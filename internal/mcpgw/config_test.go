package mcpgw

import (
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func validEnv() map[string]string {
	return map[string]string{
		"HM_MCP_UPSTREAM_URL": "https://harbormaster.internal",
		"HM_MCP_PUBLIC_URL":   "https://mcp.example.com",
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig(envFrom(validEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8481" || cfg.MaxResultBytes != 100000 || cfg.UpstreamTimeout != 30*time.Second || !cfg.TrustedProxy {
		t.Errorf("defaults wrong: %+v", cfg)
	}
	if cfg.UpstreamURL != "https://harbormaster.internal" || cfg.PublicURL != "https://mcp.example.com" {
		t.Errorf("urls wrong: %+v", cfg)
	}
	if len(cfg.AllowedOrigins) != 0 {
		t.Errorf("AllowedOrigins = %v, want none by default", cfg.AllowedOrigins)
	}
	if got := cfg.MCPURL(); got != "https://mcp.example.com/mcp" {
		t.Errorf("MCPURL = %q", got)
	}
}

func TestLoadConfig_ParsesOverrides(t *testing.T) {
	env := validEnv()
	env["HM_MCP_LISTEN"] = "127.0.0.1:9000"
	env["HM_MCP_ALLOWED_ORIGINS"] = " https://a.example , ,https://b.example"
	env["HM_MCP_TRUSTED_PROXY"] = "false"
	env["HM_MCP_MAX_RESULT_BYTES"] = "50000"
	env["HM_MCP_UPSTREAM_TIMEOUT"] = "5s"
	cfg, err := LoadConfig(envFrom(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9000" || cfg.TrustedProxy || cfg.MaxResultBytes != 50000 || cfg.UpstreamTimeout != 5*time.Second {
		t.Errorf("overrides not applied: %+v", cfg)
	}
	if strings.Join(cfg.AllowedOrigins, "|") != "https://a.example|https://b.example" {
		t.Errorf("AllowedOrigins = %v", cfg.AllowedOrigins)
	}
}

func TestLoadConfig_Validation(t *testing.T) {
	rows := []struct {
		name    string
		set     map[string]string
		unset   string
		wantVar string
	}{
		{name: "upstream required", unset: "HM_MCP_UPSTREAM_URL", wantVar: "HM_MCP_UPSTREAM_URL"},
		{name: "public required", unset: "HM_MCP_PUBLIC_URL", wantVar: "HM_MCP_PUBLIC_URL"},
		{name: "upstream http on a public host", set: map[string]string{"HM_MCP_UPSTREAM_URL": "http://harbormaster.internal"}, wantVar: "HM_MCP_UPSTREAM_URL"},
		{name: "upstream ftp", set: map[string]string{"HM_MCP_UPSTREAM_URL": "ftp://harbormaster.internal"}, wantVar: "HM_MCP_UPSTREAM_URL"},
		{name: "upstream with a path", set: map[string]string{"HM_MCP_UPSTREAM_URL": "https://harbormaster.internal/api"}, wantVar: "HM_MCP_UPSTREAM_URL"},
		{name: "upstream with user info", set: map[string]string{"HM_MCP_UPSTREAM_URL": "https://u:p@harbormaster.internal"}, wantVar: "HM_MCP_UPSTREAM_URL"},
		{name: "public http on a public host", set: map[string]string{"HM_MCP_PUBLIC_URL": "http://mcp.example.com"}, wantVar: "HM_MCP_PUBLIC_URL"},
		{name: "public with a path", set: map[string]string{"HM_MCP_PUBLIC_URL": "https://mcp.example.com/mcp"}, wantVar: "HM_MCP_PUBLIC_URL"},
		{name: "public without a host", set: map[string]string{"HM_MCP_PUBLIC_URL": "https://"}, wantVar: "HM_MCP_PUBLIC_URL"},
		{name: "listen without a port", set: map[string]string{"HM_MCP_LISTEN": "localhost"}, wantVar: "HM_MCP_LISTEN"},
		{name: "trusted proxy not a bool", set: map[string]string{"HM_MCP_TRUSTED_PROXY": "maybe"}, wantVar: "HM_MCP_TRUSTED_PROXY"},
		{name: "max result too small", set: map[string]string{"HM_MCP_MAX_RESULT_BYTES": "9999"}, wantVar: "HM_MCP_MAX_RESULT_BYTES"},
		{name: "max result too large", set: map[string]string{"HM_MCP_MAX_RESULT_BYTES": "140001"}, wantVar: "HM_MCP_MAX_RESULT_BYTES"},
		{name: "max result not a number", set: map[string]string{"HM_MCP_MAX_RESULT_BYTES": "lots"}, wantVar: "HM_MCP_MAX_RESULT_BYTES"},
		{name: "timeout too short", set: map[string]string{"HM_MCP_UPSTREAM_TIMEOUT": "999ms"}, wantVar: "HM_MCP_UPSTREAM_TIMEOUT"},
		{name: "timeout too long", set: map[string]string{"HM_MCP_UPSTREAM_TIMEOUT": "121s"}, wantVar: "HM_MCP_UPSTREAM_TIMEOUT"},
		{name: "timeout not a duration", set: map[string]string{"HM_MCP_UPSTREAM_TIMEOUT": "soon"}, wantVar: "HM_MCP_UPSTREAM_TIMEOUT"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			env := validEnv()
			for k, v := range row.set {
				env[k] = v
			}
			if row.unset != "" {
				delete(env, row.unset)
			}
			_, err := LoadConfig(envFrom(env))
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), row.wantVar) {
				t.Errorf("error %q does not name %s", err, row.wantVar)
			}
		})
	}
}

func TestLoadConfig_AcceptsLoopbackHTTPAndBounds(t *testing.T) {
	for _, host := range []string{"http://127.0.0.1:8480", "http://localhost:8480", "http://[::1]:8480"} {
		env := validEnv()
		env["HM_MCP_UPSTREAM_URL"] = host
		env["HM_MCP_PUBLIC_URL"] = host
		if _, err := LoadConfig(envFrom(env)); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	for _, bound := range []struct{ name, value string }{
		{"HM_MCP_MAX_RESULT_BYTES", "10000"}, {"HM_MCP_MAX_RESULT_BYTES", "140000"},
		{"HM_MCP_UPSTREAM_TIMEOUT", "1s"}, {"HM_MCP_UPSTREAM_TIMEOUT", "120s"},
	} {
		env := validEnv()
		env[bound.name] = bound.value
		if _, err := LoadConfig(envFrom(env)); err != nil {
			t.Errorf("%s=%s: %v", bound.name, bound.value, err)
		}
	}
}
