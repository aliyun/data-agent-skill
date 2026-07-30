package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/alibabacloud/data-agent-mcp-server/internal/config"
	"github.com/alibabacloud/data-agent-mcp-server/internal/dataagent"
	mcpserver "github.com/alibabacloud/data-agent-mcp-server/internal/mcp"
	"github.com/alibabacloud/data-agent-mcp-server/internal/session"
	"github.com/alibabacloud/data-agent-mcp-server/internal/tenant"
)

var version = "dev"

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.SetOutput(os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("shutting down...")
		cancel()
	}()

	// Configuration: env vars > .env file > YAML config file > defaults.
	config.LoadDotEnv()
	cfg, cfgPath, err := config.Load()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}
	if cfgPath != "" {
		log.Printf("loaded config from %s", cfgPath)
	}

	var cred *dataagent.Credential
	if cfg.APIKey != "" {
		cred = &dataagent.Credential{APIKey: cfg.APIKey}
		log.Println("using API Key authentication")
	} else {
		cred, err = dataagent.LoadCredential()
		if err != nil {
			log.Fatalf("failed to load credentials: %v", err)
		}
	}

	var clientOpts []dataagent.ClientOption
	if cfg.DMSUnit != "" {
		clientOpts = append(clientOpts, dataagent.WithDMSUnit(cfg.DMSUnit))
	}
	if cfg.DMSEnterpriseEndpoint != "" {
		clientOpts = append(clientOpts, dataagent.WithDMSEnterpriseEndpoint(cfg.DMSEnterpriseEndpoint))
		log.Printf("using dms-enterprise endpoint %s", cfg.DMSEnterpriseEndpoint)
	}
	if cfg.WorkspaceID != "" {
		clientOpts = append(clientOpts, dataagent.WithWorkspaceID(cfg.WorkspaceID))
	}
	client := dataagent.NewClient(cred, cfg.Region, clientOpts...)

	mgr := session.NewManager(client, cfg.SessionsDir)
	mgr.RestoreSessions(ctx)
	go mgr.RunHousekeeping(ctx)

	srv := mcpserver.New(mgr, client, version)
	srv.SetUploadRoots(cfg.Upload.AllowedDirs)

	// Identity multi-tenant mode: map upstream identity headers (e.g. Feishu
	// Aily's x-aily-user/x-aily-email) to per-user RAM roles via STS
	// AssumeRole so RAM/DMS permissions apply per end user.
	if cfg.Identity.Enabled {
		registry := tenant.NewRegistry(ctx, cfg, cred)
		srv.SetIdentityHeaders(cfg.Identity.Headers.User, cfg.Identity.Headers.Email, cfg.Identity.Headers.Token)
		srv.SetResolver(func(reqCtx context.Context) (*session.Manager, *dataagent.Client, mcpserver.SessionDefaults, error) {
			t, err := registry.Resolve(reqCtx)
			if err != nil {
				return nil, nil, mcpserver.SessionDefaults{}, err
			}
			if t == nil {
				return nil, nil, mcpserver.SessionDefaults{}, nil // default server identity
			}
			defaults := mcpserver.SessionDefaults{
				Mode:          t.Defaults.Mode,
				CustomAgentID: t.Defaults.CustomAgentID,
			}
			return t.Manager, t.Client, defaults, nil
		})
		log.Printf("identity multi-tenant mode enabled (default_group=%v, groups=%d, require_identity=%v, headers=%s/%s)",
			cfg.Identity.Default != nil && cfg.Identity.Default.RoleArn != "", len(cfg.Identity.Groups),
			cfg.Identity.RequireIdentity, cfg.Identity.Headers.User, cfg.Identity.Headers.Email)
	}

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("mcp server error: %v", err)
	}
}
