package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	"github.com/alibabacloud/data-agent-mcp-server/internal/logging"
	mcpserver "github.com/alibabacloud/data-agent-mcp-server/internal/mcp"
	"github.com/alibabacloud/data-agent-mcp-server/internal/perf"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

var version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.SetOutput(os.Stderr)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() (err error) {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := config.LoadDotEnv(); err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	cfg, _, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	transport := os.Getenv("MCP_TRANSPORT")
	if err := cfg.ValidateTransport(transport); err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}
	if cfg.Log.Enabled {
		output, openErr := logging.Open(cfg.Log.File, cfg.Log.MaxBytes)
		if openErr != nil {
			return fmt.Errorf("log file initialization failed: %w", openErr)
		}
		previous := log.Writer()
		log.SetOutput(output)
		defer func() {
			if err != nil {
				log.Printf("MCP server stopped: %v", err)
			}
			log.SetOutput(previous)
			if output.Close() != nil {
				log.Print("MCP log file close failed")
			}
		}()
		log.Printf("MCP log enabled file=%q max_bytes=%d", cfg.Log.File, cfg.Log.MaxBytes)
	}
	remote := transport == "sse" || transport == "streamable-http"
	provider, err := dataagent.NewCredentialProvider()
	if err != nil {
		return fmt.Errorf("failed to initialize credentials: %w", err)
	}

	var mgr *session.Manager
	var client *dataagent.Client
	if !remote {
		strictCredential := func() (*dataagent.Credential, error) {
			return dataagent.CredentialFromProvider(provider)
		}
		cred, err := strictCredential()
		if err != nil {
			return fmt.Errorf("failed to load credentials: %w", err)
		}
		client = dataagent.NewClient(cred, cfg.Region,
			dataagent.WithStrictCredentialProvider(strictCredential),
			dataagent.WithCredentialProvider(func() *dataagent.Credential {
				current, err := strictCredential()
				if err != nil {
					return cred // Legacy analysis only; the gateway always uses strict refresh.
				}
				return current
			}),
			dataagent.WithGatewayOptions(dataagent.GatewayOptions{
				Endpoint:         cfg.AgentGateway.Endpoint,
				RequestTimeout:   time.Duration(cfg.AgentGateway.RequestTimeoutSeconds) * time.Second,
				MaxResponseBytes: cfg.AgentGateway.MaxResponseBytes,
			}),
			dataagent.WithDMSUnit(cfg.DMSUnit),
			dataagent.WithDataAgentEndpoint(cfg.DataAgentEndpoint),
			dataagent.WithDMSEnterpriseEndpoint(cfg.DMSEnterpriseEndpoint),
			dataagent.WithWorkspaceID(cfg.WorkspaceID),
		)
		mgr, err = session.NewManager(client, cfg.SessionsDir)
		if err != nil {
			return fmt.Errorf("session storage initialization failed: %w", err)
		}
		defer mgr.Close()
		mgr.RestoreSessions(ctx)
		go mgr.RunHousekeeping(ctx)
	}

	srv := mcpserver.New(mgr, client, version)
	srv.SetCustomAgentID(cfg.CustomAgentID)
	srv.ApplyRequestLogConfig(cfg.Log.Requests)
	srv.EnableGateway(cfg.AgentGateway.Enabled, cfg.AgentGateway.DataSourcesEnabled, remote)

	var collector *perf.Collector
	if cfg.Log.Perf.Enabled {
		collector, err = perf.New(perf.Options{
			Path:     cfg.Log.Perf.File,
			Interval: time.Duration(cfg.Log.Perf.IntervalSeconds) * time.Second,
			MaxBytes: cfg.Log.Perf.MaxBytes,
			DiskPath: cfg.SessionsDir,
		})
		if err != nil {
			return fmt.Errorf("perf log: %w", err)
		}
		collector.AddSource("mcp", srv.PerfStats)
		if mgr != nil {
			collector.AddSource("sessions", func() map[string]any {
				return map[string]any{"watchers": mgr.WatcherStats()}
			})
		}
		log.Printf("perf log enabled file=%q interval=%ds", cfg.Log.Perf.File, cfg.Log.Perf.IntervalSeconds)
	}

	if remote {
		registry := tenant.NewRegistry(ctx, cfg, provider)
		defer registry.Close()
		if collector != nil {
			collector.AddSource("tenants", registry.PerfStats)
		}
		srv.SetIdentityHeaders(cfg.Identity.Headers.User, cfg.Identity.Headers.Email, cfg.Identity.Headers.Token)
		if cfg.Identity.JWT.Enabled {
			srv.EnableJWTIdentity(cfg.Identity.JWT.Header)
		}
		if err := srv.ConfigureHTTP(cfg.HTTP, registry.Authenticate); err != nil {
			return err
		}
		srv.SetResolver(func(reqCtx context.Context) (*session.Manager, *dataagent.Client, mcpserver.SessionDefaults, error) {
			t, err := registry.Resolve(reqCtx)
			if err != nil {
				return nil, nil, mcpserver.SessionDefaults{}, err
			}
			if t == nil {
				return nil, nil, mcpserver.SessionDefaults{}, fmt.Errorf("verified identity required")
			}
			return t.Manager, t.Client, mcpserver.SessionDefaults{
				Mode:          t.Defaults.Mode,
				CustomAgentID: t.Defaults.CustomAgentID,
			}, nil
		})
		log.Printf("remote identity enabled namespace=%q jwt=%t groups=%d", cfg.Identity.Namespace, cfg.Identity.JWT.Enabled, len(cfg.Identity.Groups))
	}
	if collector != nil {
		perfCtx, stopPerf := context.WithCancel(context.Background())
		perfDone := make(chan struct{})
		go func() { defer close(perfDone); collector.Run(perfCtx) }()
		defer func() { stopPerf(); <-perfDone }()
	}
	return srv.Run(ctx)
}
