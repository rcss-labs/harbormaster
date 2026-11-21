package config

import (
	"strings"
	"testing"
)

func loadWithKV(t *testing.T, env map[string]string) (Config, error) {
	t.Helper()
	env["HM_DATABASE_URL"] = "postgres://example"
	return Load(envFromMap(env))
}

func TestConfig_KVDefaults(t *testing.T) {
	cfg, err := loadWithKV(t, map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KV.URL != "" || cfg.KV.Prefix != "hm" {
		t.Fatalf("KV = %+v, want empty URL and prefix hm", cfg.KV)
	}
}

func TestConfig_KVAcceptsFourSchemes(t *testing.T) {
	for _, scheme := range []string{"redis", "rediss", "valkey", "valkeys"} {
		url := scheme + "://user:pw@cache.example:6379/0"
		cfg, err := loadWithKV(t, map[string]string{"HM_KV_URL": url})
		if err != nil {
			t.Fatalf("%s: %v", scheme, err)
		}
		if cfg.KV.URL != url {
			t.Fatalf("%s: URL = %q", scheme, cfg.KV.URL)
		}
	}
}

func TestConfig_KVRejectsOtherSchemeWithoutEchoingIt(t *testing.T) {
	_, err := loadWithKV(t, map[string]string{"HM_KV_URL": "http://user:s3cret@h"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error leaks the password: %v", err)
	}
	if err.Error() != "HM_KV_URL: scheme must be redis, rediss, valkey or valkeys" {
		t.Fatalf("error = %q", err)
	}
}

func TestConfig_KVRejectsBadPrefix(t *testing.T) {
	for _, prefix := range []string{"Hm", "a b", "x:y", strings.Repeat("a", 33)} {
		_, err := loadWithKV(t, map[string]string{"HM_KV_PREFIX": prefix})
		if err == nil || err.Error() != "HM_KV_PREFIX: use 1 to 32 lowercase letters, digits, - or _" {
			t.Fatalf("prefix %q: err = %v", prefix, err)
		}
	}
	if _, err := loadWithKV(t, map[string]string{"HM_KV_PREFIX": "lane-1_a"}); err != nil {
		t.Fatal(err)
	}
}

func TestConfig_KVRejectsDatabaseOtherThanZero(t *testing.T) {
	_, err := loadWithKV(t, map[string]string{"HM_KV_URL": "redis://user:s3cret@cache.internal:6379/3"})
	if err == nil {
		t.Fatal("expected an error for database 3")
	}
	if err.Error() != "HM_KV_URL must use database 0" {
		t.Fatalf("error = %q", err)
	}
	for _, leak := range []string{"s3cret", "cache.internal"} {
		if strings.Contains(err.Error(), leak) {
			t.Fatalf("error leaks %q", leak)
		}
	}
	for _, ok := range []string{"redis://cache.internal", "redis://cache.internal/", "redis://cache.internal/0", "valkeys://u:p@cache.internal:6380/0"} {
		if _, err := loadWithKV(t, map[string]string{"HM_KV_URL": ok}); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
}
