package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	catalogueAnthropic = `[{"provider":"anthropic","models":[{"id":"m1","input_per_mtok_usd":"3.00","output_per_mtok_usd":"15.00","context_tokens":200000}]}]`
	catalogueBedrock   = `[{"provider":"bedrock","models":[{"id":"b1","input_per_mtok_usd":"3.00","output_per_mtok_usd":"15.00","context_tokens":200000}]}]`
	// Fake keys, assembled from pieces so secret scanners do not flag this file.
	fakeKey = "sk-" + "ant-" + "not-a-real-key-0123456789"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"HM_AGENT_UPSTREAM_URL":      "https://hm.internal.example",
		"HM_AGENT_PROVIDERS":         catalogueAnthropic,
		"HM_AGENT_ANTHROPIC_API_KEY": fakeKey,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":8484" || cfg.UpstreamURL != "https://hm.internal.example" {
		t.Errorf("Listen, UpstreamURL = %q, %q", cfg.Listen, cfg.UpstreamURL)
	}
	if cfg.MaxSteps != 20 || cfg.MaxRunTokens != 200000 || cfg.RunTimeout != 5*time.Minute || cfg.MaxConcurrentRuns != 8 || cfg.MaxResultBytes != 100000 {
		t.Errorf("limits = %d, %d, %s, %d, %d", cfg.MaxSteps, cfg.MaxRunTokens, cfg.RunTimeout, cfg.MaxConcurrentRuns, cfg.MaxResultBytes)
	}
	if _, ok := cfg.Catalogue.Find("anthropic", "m1"); !ok {
		t.Error("the catalogue lost its model")
	}
	if cfg.AnthropicKey.Reveal() != fakeKey {
		t.Error("the key was not read")
	}
	if shown := fmt.Sprintf("%v %+v %#v", cfg, cfg, cfg); strings.Contains(shown, fakeKey) {
		t.Error("formatting the config prints the key")
	}
}

func TestLoadConfig_Overrides(t *testing.T) {
	cfg, err := LoadConfig(env(map[string]string{
		"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_PROVIDERS": catalogueBedrock,
		"HM_AGENT_BEDROCK_REGION": "eu-west-1", "HM_AGENT_LISTEN": "127.0.0.1:9000",
		"HM_AGENT_MAX_STEPS": "5", "HM_AGENT_MAX_RUN_TOKENS": "50000", "HM_AGENT_RUN_TIMEOUT": "90s",
		"HM_AGENT_MAX_CONCURRENT_RUNS": "2", "HM_AGENT_MAX_RESULT_BYTES": "20000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:9000" || cfg.MaxSteps != 5 || cfg.MaxRunTokens != 50000 || cfg.RunTimeout != 90*time.Second ||
		cfg.MaxConcurrentRuns != 2 || cfg.MaxResultBytes != 20000 || cfg.BedrockRegion != "eu-west-1" {
		t.Errorf("cfg = %+v", cfg)
	}
}

func TestLoadConfig_RejectsBadValuesNamingTheVariable(t *testing.T) {
	base := map[string]string{"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_PROVIDERS": catalogueAnthropic, "HM_AGENT_ANTHROPIC_API_KEY": fakeKey}
	cases := map[string]map[string]string{
		"HM_AGENT_UPSTREAM_URL":        {"HM_AGENT_UPSTREAM_URL": ""},
		"HM_AGENT_PROVIDERS":           {"HM_AGENT_PROVIDERS": "not json"},
		"HM_AGENT_LISTEN":              {"HM_AGENT_LISTEN": "nope"},
		"HM_AGENT_MAX_STEPS":           {"HM_AGENT_MAX_STEPS": "0"},
		"HM_AGENT_MAX_RUN_TOKENS":      {"HM_AGENT_MAX_RUN_TOKENS": "-1"},
		"HM_AGENT_RUN_TIMEOUT":         {"HM_AGENT_RUN_TIMEOUT": "forever"},
		"HM_AGENT_MAX_CONCURRENT_RUNS": {"HM_AGENT_MAX_CONCURRENT_RUNS": "0"},
		"HM_AGENT_MAX_RESULT_BYTES":    {"HM_AGENT_MAX_RESULT_BYTES": "5"},
		"HM_AGENT_UPSTREAM_CA_PEM":     {"HM_AGENT_UPSTREAM_CA_PEM": "not a certificate"},
	}
	for name, override := range cases {
		values := map[string]string{}
		for k, v := range base {
			values[k] = v
		}
		for k, v := range override {
			values[k] = v
		}
		_, err := LoadConfig(env(values))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v, want one naming the variable", name, err)
		} else if strings.Contains(err.Error(), fakeKey) {
			t.Errorf("%s: the error contains the key", name)
		}
	}
}

func TestLoadConfig_FakeProviderOnlyOnLoopback(t *testing.T) {
	loopback := map[string]string{"HM_AGENT_UPSTREAM_URL": "http://127.0.0.1:8080", "HM_AGENT_FAKE_PROVIDER": "true"}
	cfg, err := LoadConfig(env(loopback))
	if err != nil {
		t.Fatal(err)
	}
	model, ok := cfg.Catalogue.Find("fake", "fake-1")
	if !ok || model.InputMicroUSDPerMTok != 1_000_000 || model.OutputMicroUSDPerMTok != 1_000_000 || model.ContextTokens <= 0 {
		t.Errorf("fake-1 = %+v, %v", model, ok)
	}
	if !cfg.FakeProvider {
		t.Error("FakeProvider is false")
	}

	off := map[string]string{"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_FAKE_PROVIDER": "true"}
	if _, err := LoadConfig(env(off)); err == nil || !strings.Contains(err.Error(), "HM_AGENT_FAKE_PROVIDER") {
		t.Errorf("fake off loopback: err = %v", err)
	}
	// The fake switch may sit beside real providers on a loopback upstream.
	loopback["HM_AGENT_PROVIDERS"], loopback["HM_AGENT_ANTHROPIC_API_KEY"] = catalogueAnthropic, fakeKey
	if cfg, err = LoadConfig(env(loopback)); err != nil || len(cfg.Catalogue) != 2 {
		t.Errorf("fake beside anthropic: %v, %d entries", err, len(cfg.Catalogue))
	}
}

func TestLoadConfig_KeyWithoutCatalogueEntry(t *testing.T) {
	_, err := LoadConfig(env(map[string]string{
		"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_PROVIDERS": catalogueAnthropic,
		"HM_AGENT_ANTHROPIC_API_KEY": fakeKey, "HM_AGENT_OPENAI_API_KEY": fakeKey + "-openai",
	}))
	if err == nil || !strings.Contains(err.Error(), "HM_AGENT_OPENAI_API_KEY") || strings.Contains(err.Error(), "not-a-real-key") {
		t.Errorf("err = %v, want one naming HM_AGENT_OPENAI_API_KEY and no key value", err)
	}
	_, err = LoadConfig(env(map[string]string{
		"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_PROVIDERS": catalogueAnthropic,
		"HM_AGENT_ANTHROPIC_API_KEY": fakeKey, "HM_AGENT_BEDROCK_REGION": "eu-west-1",
	}))
	if err == nil || !strings.Contains(err.Error(), "HM_AGENT_BEDROCK_REGION") {
		t.Errorf("err = %v, want one naming HM_AGENT_BEDROCK_REGION", err)
	}
}

func TestLoadConfig_CatalogueEntryWithoutKey(t *testing.T) {
	for _, tc := range []struct{ name, catalogue, variable string }{
		{"anthropic", catalogueAnthropic, "HM_AGENT_ANTHROPIC_API_KEY"},
		{"bedrock", catalogueBedrock, "HM_AGENT_BEDROCK_REGION"},
	} {
		_, err := LoadConfig(env(map[string]string{"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_PROVIDERS": tc.catalogue}))
		if err == nil || !strings.Contains(err.Error(), tc.variable) {
			t.Errorf("%s: err = %v, want one naming %s", tc.name, err, tc.variable)
		}
	}
}

func TestLoadConfig_ErrorTextNeverContainsAKeyValue(t *testing.T) {
	for _, bad := range []map[string]string{
		{"HM_AGENT_PROVIDERS": catalogueAnthropic + "x"},
		{"HM_AGENT_PROVIDERS": catalogueBedrock},
		{"HM_AGENT_UPSTREAM_URL": "ftp://example.com"},
		{"HM_AGENT_FAKE_PROVIDER": "true"},
		{"HM_AGENT_FAKE_PROVIDER": "maybe"},
	} {
		values := map[string]string{
			"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_PROVIDERS": catalogueAnthropic,
			"HM_AGENT_ANTHROPIC_API_KEY": fakeKey, "HM_AGENT_OPENAI_API_KEY": fakeKey + "-2", "HM_AGENT_GOOGLE_API_KEY": fakeKey + "-3",
		}
		for k, v := range bad {
			values[k] = v
		}
		_, err := LoadConfig(env(values))
		if err == nil {
			t.Errorf("%v: no error", bad)
		} else if strings.Contains(err.Error(), "not-a-real-key") {
			t.Errorf("%v: the error contains a key: %v", bad, err)
		}
	}
}

func TestLoadConfig_TheFakeProviderIsNeverListedBeside(t *testing.T) {
	fakeEntry := `[{"provider":"fake","models":[{"id":"fake-1","input_per_mtok_usd":"1","output_per_mtok_usd":"1","context_tokens":1000}]}]`
	// Without the switch, a catalogue naming it is an error, whatever the upstream is.
	for _, upstream := range []string{"http://127.0.0.1:8080", "https://hm.internal.example"} {
		_, err := LoadConfig(env(map[string]string{"HM_AGENT_UPSTREAM_URL": upstream, "HM_AGENT_PROVIDERS": fakeEntry}))
		if err == nil || !strings.Contains(err.Error(), "HM_AGENT_FAKE_PROVIDER") {
			t.Errorf("%s: err = %v", upstream, err)
		}
	}
	// With the switch it would be listed twice.
	_, err := LoadConfig(env(map[string]string{"HM_AGENT_UPSTREAM_URL": "http://127.0.0.1:8080", "HM_AGENT_PROVIDERS": fakeEntry, "HM_AGENT_FAKE_PROVIDER": "true"}))
	if err == nil || !strings.Contains(err.Error(), "HM_AGENT_PROVIDERS") {
		t.Errorf("fake listed and switched on: err = %v", err)
	}
}

func TestLoadConfig_UpstreamMustBeExactlyWhatItLooksLike(t *testing.T) {
	for _, upstream := range []string{
		"http://hm.internal.example",           // plain http off loopback
		"http://127.0.0.1.evil.example",        // loopback by prefix
		"http://localhost.evil.example",        // loopback by prefix
		"http://evil-localhost",                // loopback by suffix
		"http://user:secretpw@127.0.0.1:8080",  // user info
		"https://user:secretpw@hm.example.com", // user info
		"https://hm.example.com/api",           // path
		"https://hm.example.com?x=1",           // query
		"ftp://hm.example.com",
	} {
		_, err := LoadConfig(env(map[string]string{"HM_AGENT_UPSTREAM_URL": upstream, "HM_AGENT_FAKE_PROVIDER": "true"}))
		if err == nil || !strings.Contains(err.Error(), "HM_AGENT_UPSTREAM_URL") && !strings.Contains(err.Error(), "HM_AGENT_FAKE_PROVIDER") {
			t.Errorf("%s: err = %v", upstream, err)
		}
		if err != nil && strings.Contains(err.Error(), "secretpw") {
			t.Errorf("%s: the error echoes the URL's credential", upstream)
		}
	}
	// A loopback-looking name on https is still not loopback: the fake provider stays refused.
	for _, upstream := range []string{"https://127.0.0.1.evil.example", "https://localhost.evil.example", "https://hm.example.com"} {
		_, err := LoadConfig(env(map[string]string{"HM_AGENT_UPSTREAM_URL": upstream, "HM_AGENT_FAKE_PROVIDER": "true"}))
		if err == nil || !strings.Contains(err.Error(), "HM_AGENT_FAKE_PROVIDER") {
			t.Errorf("%s: err = %v, want the fake provider refused", upstream, err)
		}
	}
	for _, upstream := range []string{"http://localhost:8080", "http://LOCALHOST:8080", "http://127.0.0.1:1", "http://[::1]:8080"} {
		if _, err := LoadConfig(env(map[string]string{"HM_AGENT_UPSTREAM_URL": upstream, "HM_AGENT_FAKE_PROVIDER": "true"})); err != nil {
			t.Errorf("%s: %v", upstream, err)
		}
	}
}
