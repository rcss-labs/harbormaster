package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent"
	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/anthropic"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/bedrock"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/google"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/openai"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func envOf(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestRun_VersionAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut, envOf(nil)); code != 0 || !strings.HasPrefix(out.String(), "harbormaster-agent ") {
		t.Errorf("version: code %d, out %q", code, out.String())
	}
	out.Reset()
	if code := run(nil, &out, &errOut, envOf(nil)); code != 2 || !strings.Contains(errOut.String(), "usage: harbormaster-agent") {
		t.Errorf("no arguments: code %d, err %q", code, errOut.String())
	}
	if code := run([]string{"bogus"}, &out, &errOut, envOf(nil)); code != 2 {
		t.Errorf("unknown command: code %d", code)
	}
}

func TestServe_ConfigurationErrorIs2AndNamesTheVariableNotTheKey(t *testing.T) {
	const key = "sk-" + "ant-" + "not-a-real-key-0123456789"
	stderr := &lockedBuffer{}
	code := serve(context.Background(), stderr, envOf(map[string]string{
		"HM_AGENT_UPSTREAM_URL": "https://hm.internal.example", "HM_AGENT_OPENAI_API_KEY": key, "HM_AGENT_ANTHROPIC_API_KEY": key + "-a",
		"HM_AGENT_PROVIDERS": `[{"provider":"anthropic","models":[{"id":"m","input_per_mtok_usd":"1","output_per_mtok_usd":"1","context_tokens":1000}]}]`,
	}))
	if code != 2 || !strings.Contains(stderr.String(), "HM_AGENT_OPENAI_API_KEY") || strings.Contains(stderr.String(), "not-a-real-key") {
		t.Errorf("code %d, stderr %s", code, stderr.String())
	}
}

func TestServe_ServesTheFakeProviderOnLoopback(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- serve(ctx, &lockedBuffer{}, envOf(map[string]string{
			"HM_AGENT_UPSTREAM_URL": upstream.URL, "HM_AGENT_FAKE_PROVIDER": "true", "HM_AGENT_LISTEN": addr,
		}))
	}()
	var body string
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/v1/catalogue")
		if err == nil {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(raw)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never served: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(body, `"fake-1"`) {
		t.Errorf("catalogue = %s", body)
	}
	if code := run([]string{"healthcheck"}, io.Discard, io.Discard, envOf(map[string]string{"HM_AGENT_LISTEN": addr})); code != 0 {
		t.Errorf("healthcheck against a serving agent = %d", code)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code %d after shutdown", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
}

func TestHealthcheck_FailsWhenNothingAnswers(t *testing.T) {
	listener, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := listener.Addr().String()
	listener.Close()
	if code := healthcheck(io.Discard, envOf(map[string]string{"HM_AGENT_LISTEN": addr})); code != 1 {
		t.Errorf("healthcheck = %d", code)
	}
}

func TestServeListener_ShutdownLetsARunningStreamFinish(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		close(started)
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, "last\n")
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- serveListener(ctx, listener, handler, discardLogger(), 5*time.Second) }()

	got := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String() + "/")
		if err != nil {
			got <- "error: " + err.Error()
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		got <- string(raw)
	}()
	<-started
	cancel()
	if body := <-got; body != "first\nlast\n" {
		t.Errorf("the stream was cut short: %q", body)
	}
	if code := <-done; code != 0 {
		t.Errorf("exit code %d after a clean drain", code)
	}
}

func TestServeListener_ShutdownIsBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- serveListener(ctx, listener, handler, discardLogger(), 300*time.Millisecond) }()
	go func() {
		if resp, err := http.Get("http://" + listener.Addr().String() + "/"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-started
	cancel()
	select {
	case code := <-done:
		if code != 1 {
			t.Errorf("exit code %d, want 1 after a forced close", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown took longer than its bound")
	}
}

// Each key a provider's own name implies: these are fake values, assembled so scanners pass.
const (
	keyAnthropic = "key-" + "anthropic-0001"
	keyOpenAI    = "key-" + "openai-0002"
	keyGoogle    = "key-" + "google-0003"
)

func catalogueOf(ids ...string) provider.Catalogue {
	var c provider.Catalogue
	for _, id := range ids {
		c = append(c, provider.CatalogueEntry{Provider: id, Models: []provider.Model{{ID: "m", InputMicroUSDPerMTok: 1, OutputMicroUSDPerMTok: 1, ContextTokens: 1000}}})
	}
	return c
}

type namedProvider string

func (n namedProvider) Name() string { return string(n) }
func (namedProvider) Generate(context.Context, provider.Request, func(string)) (provider.Response, error) {
	return provider.Response{}, nil
}

// recordConstructors swaps the provider constructors for ones that record their Config and restores them after.
func recordConstructors(t *testing.T) (anth *anthropic.Config, oai *openai.Config, goog *google.Config, bed *bedrock.Config) {
	t.Helper()
	oldA, oldO, oldG, oldB := newAnthropic, newOpenAI, newGoogle, newBedrock
	t.Cleanup(func() { newAnthropic, newOpenAI, newGoogle, newBedrock = oldA, oldO, oldG, oldB })
	anth, oai, goog, bed = new(anthropic.Config), new(openai.Config), new(google.Config), new(bedrock.Config)
	newAnthropic = func(c anthropic.Config) (provider.Provider, error) { *anth = c; return namedProvider("anthropic"), nil }
	newOpenAI = func(c openai.Config) (provider.Provider, error) { *oai = c; return namedProvider("openai"), nil }
	newGoogle = func(c google.Config) (provider.Provider, error) { *goog = c; return namedProvider("google"), nil }
	newBedrock = func(c bedrock.Config) (provider.Provider, error) { *bed = c; return namedProvider("bedrock"), nil }
	return
}

func TestBuildProviders_EachProviderGetsItsOwnCredentialAndNoEndpoint(t *testing.T) {
	anth, oai, goog, bed := recordConstructors(t)
	cfg := agent.Config{
		Catalogue:    catalogueOf("anthropic", "openai", "google", "bedrock", "fake"),
		AnthropicKey: agent.NewSecret(keyAnthropic), OpenAIKey: agent.NewSecret(keyOpenAI), GoogleKey: agent.NewSecret(keyGoogle),
		BedrockRegion: "eu-west-1", FakeProvider: true,
	}
	providers, err := buildProviders(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"anthropic", "openai", "google", "bedrock", "fake"} {
		if providers[id] == nil || providers[id].Name() != id {
			t.Errorf("provider %s = %v", id, providers[id])
		}
	}
	if anth.APIKey != keyAnthropic || oai.APIKey != keyOpenAI || goog.APIKey != keyGoogle || bed.Region != "eu-west-1" {
		t.Errorf("credentials crossed: anthropic=%q openai=%q google=%q bedrock=%q", anth.APIKey, oai.APIKey, goog.APIKey, bed.Region)
	}
	// Nothing the environment says can move a provider to another host.
	if anth.Endpoint != "" || anth.HTTPClient != nil || oai.BaseURL != "" || goog.BaseURL != "" || goog.HTTPClient != nil ||
		bed.Endpoint != "" || bed.HTTPClient != nil || bed.Credentials != nil {
		t.Errorf("an endpoint or client override was set: %+v %+v %+v %+v", anth, oai, goog, bed)
	}
}

func TestBuildProviders_OnlyTheCataloguesProvidersAreBuilt(t *testing.T) {
	recordConstructors(t)
	providers, err := buildProviders(agent.Config{Catalogue: catalogueOf("openai"), OpenAIKey: agent.NewSecret(keyOpenAI)})
	if err != nil || len(providers) != 1 || providers["openai"] == nil {
		t.Errorf("providers = %v, err = %v", providers, err)
	}
}

func TestBuildProviders_ABuildErrorNamesTheVariableNotTheKey(t *testing.T) {
	recordConstructors(t)
	leaky := func(key string) error { return errors.New("could not use key " + key) }
	newAnthropic = func(c anthropic.Config) (provider.Provider, error) { return nil, leaky(c.APIKey) }
	newOpenAI = func(c openai.Config) (provider.Provider, error) { return nil, leaky(c.APIKey) }
	newGoogle = func(c google.Config) (provider.Provider, error) { return nil, leaky(c.APIKey) }
	newBedrock = func(c bedrock.Config) (provider.Provider, error) { return nil, leaky(c.Region) }
	for id, variable := range map[string]string{
		"anthropic": "HM_AGENT_ANTHROPIC_API_KEY", "openai": "HM_AGENT_OPENAI_API_KEY", "google": "HM_AGENT_GOOGLE_API_KEY", "bedrock": "HM_AGENT_BEDROCK_REGION",
	} {
		_, err := buildProviders(agent.Config{
			Catalogue:    catalogueOf(id),
			AnthropicKey: agent.NewSecret(keyAnthropic), OpenAIKey: agent.NewSecret(keyOpenAI), GoogleKey: agent.NewSecret(keyGoogle), BedrockRegion: "eu-west-1",
		})
		if err == nil || !strings.Contains(err.Error(), variable) {
			t.Errorf("%s: err = %v", id, err)
			continue
		}
		for _, key := range []string{keyAnthropic, keyOpenAI, keyGoogle} {
			if strings.Contains(err.Error(), key) {
				t.Errorf("%s: the build error contains a key: %v", id, err)
			}
		}
	}
}

func TestBuildProviders_TheRealConstructorsAcceptTheConfigWeGive(t *testing.T) {
	providers, err := buildProviders(agent.Config{
		Catalogue:    catalogueOf("anthropic", "openai", "google", "bedrock"),
		AnthropicKey: agent.NewSecret(keyAnthropic), OpenAIKey: agent.NewSecret(keyOpenAI), GoogleKey: agent.NewSecret(keyGoogle), BedrockRegion: "eu-west-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	for id, p := range providers {
		if p.Name() != id {
			t.Errorf("%s built a provider named %s", id, p.Name())
		}
	}
}

// The command never reads an endpoint or base URL for a provider from anywhere.
func TestMainSetsNoProviderEndpoint(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for number, line := range strings.Split(string(source), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		for _, banned := range []string{"BaseURL", "Endpoint", "HTTPClient", "Credentials:"} {
			if strings.Contains(line, banned) {
				t.Errorf("main.go:%d mentions %s", number+1, banned)
			}
		}
	}
}

func TestServeListener_CutOffHandlersGetAMomentToLog(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	logged := make(chan struct{})
	started := make(chan struct{})
	var finished bool
	var mu sync.Mutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		time.Sleep(200 * time.Millisecond) // the run's closing log line
		mu.Lock()
		finished = true
		mu.Unlock()
		close(logged)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- serveListener(ctx, listener, handler, discardLogger(), 200*time.Millisecond) }()
	go func() {
		if resp, err := http.Get("http://" + listener.Addr().String() + "/"); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	<-started
	cancel()
	if code := <-done; code != 1 {
		t.Errorf("exit code %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if !finished {
		t.Error("serveListener returned before the cut-off handler finished logging")
	}
}
