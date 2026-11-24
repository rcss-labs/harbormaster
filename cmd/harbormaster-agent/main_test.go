package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
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
