// Command harbormaster-agent is the read-only AI agent service. It holds the
// model providers' API keys, accepts only run tokens from Harbormaster, and
// reads the estate through Harbormaster's own API with the caller's token.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent"
	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/anthropic"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/bedrock"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/google"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/openai"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
)

// Overridden at build time via -ldflags "-X main.version=... -X main.commit=...".
var (
	version = "dev"
	commit  = "none"
)

const usageText = `usage: harbormaster-agent <command>

commands:
  serve        serve the agent API on HM_AGENT_LISTEN
  healthcheck  exit 0 when the local agent answers /healthz, 1 otherwise
  version      print the version and commit

environment for serve:
  HM_AGENT_UPSTREAM_URL (required; Harbormaster's private base URL), HM_AGENT_UPSTREAM_CA_PEM (optional)
  HM_AGENT_PROVIDERS (required; JSON catalogue of providers, models and prices)
  HM_AGENT_ANTHROPIC_API_KEY, HM_AGENT_OPENAI_API_KEY, HM_AGENT_GOOGLE_API_KEY, HM_AGENT_BEDROCK_REGION
    (each needed exactly when its provider is in the catalogue)
  HM_AGENT_LISTEN (default :8484)
  HM_AGENT_TRUSTED_PROXY (default true; exactly one proxy, Harbormaster's internal load balancer, appends the caller's address to X-Forwarded-For)
  HM_AGENT_MAX_STEPS (20), HM_AGENT_MAX_RUN_TOKENS (200000), HM_AGENT_RUN_TIMEOUT (5m),
  HM_AGENT_MAX_CONCURRENT_RUNS (8), HM_AGENT_MAX_RESULT_BYTES (100000)
  HM_AGENT_FAKE_PROVIDER (default false; a canned model, only with a loopback upstream)
`

const (
	readHeaderTimeout  = 10 * time.Second
	readTimeout        = 30 * time.Second
	idleTimeout        = 120 * time.Second
	shutdownGrace      = 20 * time.Second
	healthcheckTimeout = 3 * time.Second
	defaultListen      = ":8484"
	// handlerDrainWait is how long, after a forced close, running handlers get to write their last log lines.
	handlerDrainWait = 2 * time.Second
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv)) }

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "harbormaster-agent", version, commit)
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

// serve runs the agent until ctx ends, then shuts down gracefully.
// Exit codes: 0 after a clean shutdown, 1 on a runtime failure, 2 on a
// configuration error (the message names the variable, never a key).
func serve(ctx context.Context, stderr io.Writer, getenv func(string) string) int {
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	hmtools.Version = version

	cfg, err := agent.LoadConfig(getenv)
	if err != nil {
		logger.Error("invalid configuration", "err", err.Error())
		return 2
	}
	upstream, err := agent.NewUpstream(cfg)
	if err != nil {
		logger.Error("invalid configuration", "err", "HM_AGENT_UPSTREAM_URL: "+err.Error())
		return 2
	}
	providers, err := buildProviders(cfg)
	if err != nil {
		logger.Error("invalid configuration", "err", err.Error())
		return 2
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Error("listen failed", "listen", cfg.Listen, "err", err.Error())
		return 1
	}
	var offered []string
	for id := range providers {
		offered = append(offered, id)
	}
	logger.Info("harbormaster-agent starting", "version", version, "listen", listener.Addr().String(), "providers", offered)
	return serveListener(ctx, listener, agent.NewHandler(cfg, upstream, providers, logger), logger, shutdownGrace)
}

// The provider constructors, variables so a test can see what each is given.
var (
	newAnthropic = anthropic.New
	newOpenAI    = openai.New
	newGoogle    = google.New
	newBedrock   = bedrock.New
)

// buildProviders builds each catalogue provider whose credential is set. The
// provider packages pin their own endpoints: nothing here sets one (a test
// reads this file to keep it so).
func buildProviders(cfg agent.Config) (map[string]provider.Provider, error) {
	providers := map[string]provider.Provider{}
	for _, entry := range cfg.Catalogue {
		var (
			built provider.Provider
			err   error
		)
		switch entry.Provider {
		case "anthropic":
			built, err = newAnthropic(anthropic.Config{APIKey: cfg.AnthropicKey.Reveal()})
		case "openai":
			built, err = newOpenAI(openai.Config{APIKey: cfg.OpenAIKey.Reveal()})
		case "google":
			built, err = newGoogle(google.Config{APIKey: cfg.GoogleKey.Reveal()})
		case "bedrock":
			built, err = newBedrock(bedrock.Config{Region: cfg.BedrockRegion})
		case "fake":
			built = fake.Demo()
		default:
			return nil, fmt.Errorf("HM_AGENT_PROVIDERS: unknown provider %q", entry.Provider)
		}
		if err != nil {
			variable, _ := cfg.Credential(entry.Provider)
			// The error text is left out: it could echo configuration.
			return nil, fmt.Errorf("%s: the %s provider could not be built", variable, entry.Provider)
		}
		providers[entry.Provider] = built
	}
	return providers, nil
}

// serveListener serves handler on l until ctx ends, then stops accepting and
// lets running streams finish for up to grace. A stream still running after
// that is cut off and the exit code is 1.
func serveListener(ctx context.Context, l net.Listener, handler http.Handler, logger *slog.Logger, grace time.Duration) int {
	var inflight atomic.Int64
	counted := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		handler.ServeHTTP(w, r)
	})
	server := &http.Server{
		Handler:           counted,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		// No WriteTimeout: a run streams for minutes. The handler sets a deadline per write instead.
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(l) }()

	select {
	case err := <-serveErr:
		logger.Error("server stopped", "err", err.Error())
		return 1
	case <-ctx.Done():
	}
	logger.Info("harbormaster-agent shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
	defer cancel()
	code := 0
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown incomplete: closing running streams", "err", err.Error())
		_ = server.Close()
		code = 1
		// Close does not wait for handlers: give the cut-off runs a moment to log how they ended.
		deadline := time.Now().Add(handlerDrainWait)
		for inflight.Load() > 0 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err.Error())
		return 1
	}
	return code
}

// healthcheck asks the local agent's /healthz on the port of HM_AGENT_LISTEN.
// This loopback client is the one HTTP client outside the upstream and the providers.
func healthcheck(stderr io.Writer, getenv func(string) string) int {
	listen := getenv("HM_AGENT_LISTEN")
	if listen == "" {
		listen = defaultListen
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		fmt.Fprintln(stderr, "HM_AGENT_LISTEN must be host:port")
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
