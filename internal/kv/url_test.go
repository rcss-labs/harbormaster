package kv

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseURL_SchemesAndAliases(t *testing.T) {
	tests := []struct {
		raw  string
		want Options
	}{
		{"redis://cache.example", Options{Address: "cache.example:6379"}},
		{"valkey://cache.example:7000", Options{Address: "cache.example:7000"}},
		{"rediss://cache.example", Options{Address: "cache.example:6379", TLS: true}},
		{"valkeys://cache.example:6380/3", Options{Address: "cache.example:6380", TLS: true, DB: 3}},
		{"redis://:pw@cache.example/2", Options{Address: "cache.example:6379", Password: "pw", DB: 2}},
		{"redis://alice:pw@127.0.0.1:6379", Options{Address: "127.0.0.1:6379", Username: "alice", Password: "pw"}},
		{"redis://[::1]:6379/0", Options{Address: "[::1]:6379"}},
	}
	for _, tt := range tests {
		got, err := ParseURL(tt.raw)
		if err != nil {
			t.Fatalf("%s: %v", tt.raw, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %+v, want %+v", tt.raw, got, tt.want)
		}
	}
}

func TestParseURL_ErrorsNeverContainThePassword(t *testing.T) {
	bad := []string{
		"http://user:s3cret@host",
		"redis://user:s3cret@host:notaport",
		"redis://user:s3cret@host/notanumber",
		"redis://user:s3cret@host/-1",
		"redis://user:s3cret@",
		"redis://user:s3cret@host:6379/1/2",
		"redis://user:s3cret@ho st",
		"://user:s3cret@host",
		"redis://user:s3cret@host?x=%zz",
	}
	for _, raw := range bad {
		_, err := ParseURL(raw)
		if err == nil {
			t.Errorf("%s: expected an error", raw)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "host") {
			t.Errorf("%s: error leaks the URL: %v", raw, err)
		}
	}
}

func TestScrub_RemovesURLAndPassword(t *testing.T) {
	raw := "redis://alice:s3cret@cache.example:6379/1"
	cause := errors.New(`dial redis://alice:s3cret@cache.example:6379/1: auth failed for s3cret`)
	got := Scrub(cause, raw)
	if strings.Contains(got.Error(), "s3cret") || strings.Contains(got.Error(), raw) {
		t.Fatalf("scrubbed error still leaks: %v", got)
	}
	if !errors.Is(got, cause) {
		t.Fatal("Scrub should keep the cause reachable with errors.Is")
	}
	if Scrub(nil, raw) != nil {
		t.Fatal("Scrub(nil) must be nil")
	}
}
