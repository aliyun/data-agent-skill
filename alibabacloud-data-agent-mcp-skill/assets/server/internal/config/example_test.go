package config

import "testing"

// The shipped example must stay loadable by the current struct definitions;
// a key renamed in code but not in the template silently loses its value.
func TestShippedExampleConfigLoads(t *testing.T) {
	t.Setenv("DATA_AGENT_CONFIG", "../../config.yaml.example")

	cfg, path, err := Load()
	if err != nil {
		t.Fatalf("config.yaml.example failed to load: %v", err)
	}
	t.Logf("loaded %s", path)

	if cfg.Region != "cn-hangzhou" {
		t.Errorf("region = %q, want cn-hangzhou", cfg.Region)
	}
	// Left unset in the template so the transport-derived default applies.
	if cfg.Log.Requests != "" {
		t.Errorf("log.requests = %q, want empty in the template", cfg.Log.Requests)
	}
	// The endpoint keys must exist and stay empty so the region defaults apply.
	for name, got := range map[string]string{
		"data_agent_endpoint":     cfg.DataAgentEndpoint,
		"dms_enterprise_endpoint": cfg.DMSEnterpriseEndpoint,
		"api_key_endpoint":        cfg.APIKeyEndpoint,
		"api_key_stream_endpoint": cfg.APIKeyStreamEndpoint,
	} {
		if got != "" {
			t.Errorf("%s = %q, want empty in the template", name, got)
		}
	}
	if cfg.Upload.AllowedDirs != nil && len(cfg.Upload.AllowedDirs) != 0 {
		t.Errorf("upload.allowed_dirs = %v, want empty in the template", cfg.Upload.AllowedDirs)
	}
}
