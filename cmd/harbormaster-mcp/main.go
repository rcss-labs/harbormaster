// Command harbormaster-mcp is the public, read-only MCP gateway in front of
// Harbormaster's private API. It holds no credential: every call is made with
// the caller's own token.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rcss-labs/harbormaster/internal/mcpgw"
)

// Overridden at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

const usageText = `usage: harbormaster-mcp <command>

commands:
  serve        serve MCP on HM_MCP_LISTEN, forwarding to HM_MCP_UPSTREAM_URL
  healthcheck  exit 0 when the local gateway answers /healthz, 1 otherwise
  version      print the version and commit

environment for serve:
  HM_MCP_UPSTREAM_URL (required), HM_MCP_PUBLIC_URL (required)
  HM_MCP_LISTEN (default :8481), HM_MCP_ALLOWED_ORIGINS, HM_MCP_TRUSTED_PROXY (default true)
  HM_MCP_MAX_RESULT_BYTES (default 100000), HM_MCP_UPSTREAM_TIMEOUT (default 30s)
`

const (
	readHeaderTimeout  = 10 * time.Second
	readTimeout        = 30 * time.Second
	idleTimeout        = 120 * time.Second
	shutdownTimeout    = 20 * time.Second
	healthcheckTimeout = 3 * time.Second
	defaultListen      = ":8481"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "harbormaster-mcp", version, commit)
		return 0
	case "healthcheck":
		return healthcheck(stderr, getenv)
	case "serve":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		return serve(ctx, stderr, getenv)
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	}
	fmt.Fprint(stderr, usageText)
	return 2
}

// serve runs the gateway until ctx ends, then shuts down gracefully.
// Exit codes: 0 after a clean shutdown, 1 on a runtime failure, 2 on a
// configuration error (the message names the variable).
func serve(ctx context.Context, stderr io.Writer, getenv func(string) string) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	mcpgw.Version = version

	cfg, err := mcpgw.LoadConfig(getenv)
	if err != nil {
		logger.Error("invalid configuration", "err", err.Error())
		return 2
	}
	upstream, err := mcpgw.NewUpstream(cfg.UpstreamURL, cfg.UpstreamTimeout, nil)
	if err != nil {
		logger.Error("invalid configuration", "err", "HM_MCP_UPSTREAM_URL: "+err.Error())
		return 2
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listen failed", "listen", cfg.Listen, "err", err.Error())
		return 1
	}
	upstreamHost := ""
	if parsed, err := url.Parse(cfg.UpstreamURL); err == nil {
		upstreamHost = parsed.Host
	}
	logger.Info("harbormaster-mcp starting",
		"version", version, "listen", listener.Addr().String(), "upstream_host", upstreamHost, "public_url", cfg.PublicURL)

	server := &http.Server{
		Handler:           mcpgw.NewHandler(cfg, upstream, logger),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	select {
	case err := <-serveErr:
		logger.Error("server stopped", "err", err.Error())
		return 1
	case <-ctx.Done():
	}
	logger.Info("harbormaster-mcp shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown incomplete", "err", err.Error())
		return 1
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err.Error())
		return 1
	}
	return 0
}

// healthcheck asks the local gateway's /healthz on the port of HM_MCP_LISTEN.
// This loopback client is the one HTTP client outside internal/mcpgw/upstream.go.
func healthcheck(stderr io.Writer, getenv func(string) string) int {
	listen := getenv("HM_MCP_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		fmt.Fprintln(stderr, "HM_MCP_LISTEN must be host:port")
		return 1
	}
	client := &http.Client{
		Timeout:       healthcheckTimeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Get("http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz")
	if err != nil {
		fmt.Fprintln(stderr, "healthcheck failed:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "healthcheck failed: status", resp.StatusCode)
		return 1
	}
	return 0
}
