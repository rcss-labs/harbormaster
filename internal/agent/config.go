package agent

import (
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/hmtools"
)

// Defaults and bounds for the agent's settings.
const (
	defaultListen            = ":8484"
	defaultMaxSteps          = 20
	maxMaxSteps              = 100
	defaultMaxRunTokens      = 200000
	minMaxRunTokens          = 1000
	maxMaxRunTokens          = 2000000
	defaultRunTimeout        = 5 * time.Minute
	minRunTimeout            = 5 * time.Second
	maxRunTimeout            = 30 * time.Minute
	defaultMaxConcurrentRuns = 8
	maxMaxConcurrentRuns     = 256
	defaultMaxResultBytes    = 100000
	minMaxResultBytes        = 10000
	maxMaxResultBytes        = 140000
)

// Secret is a credential. Its value sits behind a function, so reflection-
// based printing shows only a func pointer, and every fmt verb prints
// [redacted]. Reveal is the only way to read it.
type Secret struct{ reveal func() string }

// NewSecret holds value as a Secret.
func NewSecret(value string) Secret { return Secret{reveal: func() string { return value }} }

func (s Secret) Reveal() string {
	if s.reveal == nil {
		return ""
	}
	return s.reveal()
}

// IsSet reports whether the secret holds a value.
func (s Secret) IsSet() bool { return s.Reveal() != "" }

func (Secret) Format(f fmt.State, _ rune)   { _, _ = io.WriteString(f, "[redacted]") }
func (Secret) String() string               { return "[redacted]" }
func (Secret) GoString() string             { return "[redacted]" }
func (Secret) MarshalJSON() ([]byte, error) { return []byte(`"[redacted]"`), nil }
func (Secret) MarshalText() ([]byte, error) { return []byte("[redacted]"), nil }

// Config is the agent's whole configuration. Its keys are Secrets, so a
// formatted or logged Config shows none.
type Config struct {
	Listen        string
	UpstreamURL   string
	UpstreamCAPEM string
	// Catalogue holds every model the operator listed, whether or not its
	// provider has a credential; the handler offers only built providers.
	Catalogue     provider.Catalogue
	AnthropicKey  Secret
	OpenAIKey     Secret
	GoogleKey     Secret
	BedrockRegion string
	FakeProvider  bool

	MaxSteps          int
	MaxRunTokens      int64
	RunTimeout        time.Duration
	MaxConcurrentRuns int
	MaxResultBytes    int
}

// Credential is the environment variable that configures a provider, and
// whether it is set.
func (c Config) Credential(providerID string) (variable string, set bool) {
	switch providerID {
	case "anthropic":
		return "HM_AGENT_ANTHROPIC_API_KEY", c.AnthropicKey.IsSet()
	case "openai":
		return "HM_AGENT_OPENAI_API_KEY", c.OpenAIKey.IsSet()
	case "google":
		return "HM_AGENT_GOOGLE_API_KEY", c.GoogleKey.IsSet()
	case "bedrock":
		return "HM_AGENT_BEDROCK_REGION", c.BedrockRegion != ""
	case "fake":
		return "HM_AGENT_FAKE_PROVIDER", c.FakeProvider
	}
	return "", false
}

var credentialedProviders = []string{"anthropic", "openai", "google", "bedrock"}

// LoadConfig reads the HM_AGENT_* settings through getenv and validates them.
// Every error names the variable at fault and never repeats a key.
func LoadConfig(getenv func(string) string) (Config, error) {
	cfg := Config{
		Listen:            defaultListen,
		MaxSteps:          defaultMaxSteps,
		MaxRunTokens:      defaultMaxRunTokens,
		RunTimeout:        defaultRunTimeout,
		MaxConcurrentRuns: defaultMaxConcurrentRuns,
		MaxResultBytes:    defaultMaxResultBytes,
	}
	read := func(name string) string { return strings.TrimSpace(getenv(name)) }

	if listen := read("HM_AGENT_LISTEN"); listen != "" {
		if _, _, err := net.SplitHostPort(listen); err != nil {
			return Config{}, fmt.Errorf("HM_AGENT_LISTEN must be host:port: %w", err)
		}
		cfg.Listen = listen
	}
	upstream, err := hmtools.ParseBaseURL("HM_AGENT_UPSTREAM_URL", getenv("HM_AGENT_UPSTREAM_URL"))
	if err != nil {
		return Config{}, err
	}
	cfg.UpstreamURL = upstream
	if raw := read("HM_AGENT_UPSTREAM_CA_PEM"); raw != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(raw)) {
			return Config{}, fmt.Errorf("HM_AGENT_UPSTREAM_CA_PEM must hold at least one PEM-encoded CA certificate")
		}
		cfg.UpstreamCAPEM = raw
	}

	if raw := read("HM_AGENT_FAKE_PROVIDER"); raw != "" {
		fakeOn, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HM_AGENT_FAKE_PROVIDER must be true or false, got %q", raw)
		}
		if fakeOn && !upstreamIsLoopback(upstream) {
			return Config{}, fmt.Errorf("HM_AGENT_FAKE_PROVIDER=true is allowed only when HM_AGENT_UPSTREAM_URL is a loopback address")
		}
		cfg.FakeProvider = fakeOn
	}

	if raw := read("HM_AGENT_PROVIDERS"); raw != "" {
		cfg.Catalogue, err = provider.ParseCatalogue(raw)
		if err != nil {
			return Config{}, fmt.Errorf("HM_AGENT_PROVIDERS: %w", err)
		}
	} else if !cfg.FakeProvider {
		return Config{}, fmt.Errorf("HM_AGENT_PROVIDERS is required")
	}
	if cfg.FakeProvider {
		for _, entry := range cfg.Catalogue {
			if entry.Provider == "fake" {
				return Config{}, fmt.Errorf("HM_AGENT_PROVIDERS must not list the fake provider: HM_AGENT_FAKE_PROVIDER adds it")
			}
		}
		cfg.Catalogue = append(cfg.Catalogue, provider.CatalogueEntry{Provider: "fake", Models: []provider.Model{
			{ID: "fake-1", InputMicroUSDPerMTok: 1_000_000, OutputMicroUSDPerMTok: 1_000_000, ContextTokens: 100_000},
		}})
	} else {
		for _, entry := range cfg.Catalogue {
			if entry.Provider == "fake" {
				return Config{}, fmt.Errorf("HM_AGENT_PROVIDERS lists the fake provider, which needs HM_AGENT_FAKE_PROVIDER=true")
			}
		}
	}

	cfg.AnthropicKey = NewSecret(read("HM_AGENT_ANTHROPIC_API_KEY"))
	cfg.OpenAIKey = NewSecret(read("HM_AGENT_OPENAI_API_KEY"))
	cfg.GoogleKey = NewSecret(read("HM_AGENT_GOOGLE_API_KEY"))
	cfg.BedrockRegion = read("HM_AGENT_BEDROCK_REGION")
	if region := cfg.BedrockRegion; region != "" && !validRegion(region) {
		return Config{}, fmt.Errorf("HM_AGENT_BEDROCK_REGION must look like eu-west-1")
	}

	// A credential and a catalogue entry go together, in both directions.
	listed := map[string]bool{}
	for _, entry := range cfg.Catalogue {
		listed[entry.Provider] = true
	}
	for _, id := range credentialedProviders {
		variable, set := cfg.Credential(id)
		switch {
		case listed[id] && !set:
			return Config{}, fmt.Errorf("HM_AGENT_PROVIDERS lists %s but %s is not set", id, variable)
		case !listed[id] && set:
			return Config{}, fmt.Errorf("%s is set but HM_AGENT_PROVIDERS lists no %s models", variable, id)
		}
	}

	if cfg.MaxSteps, err = readInt(read, "HM_AGENT_MAX_STEPS", cfg.MaxSteps, 1, maxMaxSteps); err != nil {
		return Config{}, err
	}
	maxTokens, err := readInt(read, "HM_AGENT_MAX_RUN_TOKENS", int(cfg.MaxRunTokens), minMaxRunTokens, maxMaxRunTokens)
	if err != nil {
		return Config{}, err
	}
	cfg.MaxRunTokens = int64(maxTokens)
	if cfg.MaxConcurrentRuns, err = readInt(read, "HM_AGENT_MAX_CONCURRENT_RUNS", cfg.MaxConcurrentRuns, 1, maxMaxConcurrentRuns); err != nil {
		return Config{}, err
	}
	if cfg.MaxResultBytes, err = readInt(read, "HM_AGENT_MAX_RESULT_BYTES", cfg.MaxResultBytes, minMaxResultBytes, maxMaxResultBytes); err != nil {
		return Config{}, err
	}
	if raw := read("HM_AGENT_RUN_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout < minRunTimeout || timeout > maxRunTimeout {
			return Config{}, fmt.Errorf("HM_AGENT_RUN_TIMEOUT must be a duration between %s and %s, got %q", minRunTimeout, maxRunTimeout, raw)
		}
		cfg.RunTimeout = timeout
	}
	return cfg, nil
}

func readInt(read func(string) string, name string, def, minimum, maximum int) (int, error) {
	raw := read(name)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < minimum || n > maximum {
		return 0, fmt.Errorf("%s must be a number between %d and %d, got %q", name, minimum, maximum, raw)
	}
	return n, nil
}

// validRegion accepts the shape of an AWS region id, such as eu-west-1.
func validRegion(region string) bool {
	if len(region) > 30 {
		return false
	}
	for _, r := range region {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return strings.Contains(region, "-")
}

func upstreamIsLoopback(base string) bool {
	parsed, err := url.Parse(base)
	return err == nil && hmtools.IsLoopbackHost(parsed.Hostname())
}

// upstreamTimeout bounds one tool call to Harbormaster.
const upstreamTimeout = 30 * time.Second

// NewUpstream is the agent's one client for Harbormaster, sending its own User-Agent.
func NewUpstream(cfg Config) (*hmtools.Upstream, error) {
	return hmtools.NewUpstreamForProduct(cfg.UpstreamURL, upstreamTimeout, cfg.UpstreamCAPEM, "harbormaster-agent")
}
