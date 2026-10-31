package main

import (
	"strings"
	"testing"
)

func TestDatabaseURLFromSecret(t *testing.T) {
	const pw = "S3cr3t PW!/@"
	tests := []struct {
		name, secret, want, wantErr string
	}{
		{"plain url", "postgres://u:p@h:5432/d?sslmode=disable\n", "postgres://u:p@h:5432/d?sslmode=disable", ""},
		{"full json numeric port", `{"username":"hm","password":"` + pw + `","host":"db.example","port":5433,"dbname":"app"}`,
			"postgres://hm:S3cr3t%20PW%21%2F%40@db.example:5433/app?sslmode=require", ""},
		{"full json string port", `{"username":"hm","password":"p","host":"db.example","port":"5434","dbname":"app","sslmode":"disable"}`,
			"postgres://hm:p@db.example:5434/app?sslmode=disable", ""},
		{"defaults", `{"username":"hm","password":"p","host":"db.example"}`,
			"postgres://hm:p@db.example:5432/postgres?sslmode=require", ""},
		{"json without host", `{"username":"hm","password":"` + pw + `"}`, "", "missing host"},
		{"malformed json", `{"username":"hm","password":"` + pw, "", "not valid"},
		{"non-url text", "password=" + pw + " host=x", "", "not a postgres URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := databaseURLFromSecret(tt.secret)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v; want containing %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "S3cr3t") || strings.Contains(err.Error(), "username") {
					t.Fatalf("error leaks secret content: %v", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
