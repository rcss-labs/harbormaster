package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// runHealthcheck GETs /healthz on the local server (port from HM_LISTEN) and fails unless it returns 200.
func runHealthcheck(ctx context.Context, getenv func(string) string) error {
	listen := getenv("HM_LISTEN")
	if listen == "" {
		listen = ":8480"
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("HM_LISTEN %q: %w", listen, err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}
