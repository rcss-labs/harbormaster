package main

import (
	"bytes"
	"context"
	"net"
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

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// startServing runs serve in the background and returns its listen address and a stop function that
// reports the exit code.
func startServing(t *testing.T, env map[string]string) (addr string, stop func() int) {
	t.Helper()
	addr = freeAddr(t)
	env["HM_MCP_LISTEN"] = addr
	ctx, cancel := context.WithCancel(context.Background())
	stderr := &lockedBuffer{}
	done := make(chan int, 1)
	go func() { done <- serve(ctx, stderr, envOf(env)) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("gateway never listened; log: %s", stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	stopped := false
	stop = func() int {
		stopped = true
		cancel()
		select {
		case code := <-done:
			return code
		case <-time.After(10 * time.Second):
			t.Fatal("serve did not stop")
			return -1
		}
	}
	t.Cleanup(func() {
		if !stopped {
			stop()
		}
	})
	return addr, stop
}

func TestServe_ConfigErrorExits2NamingTheVariable(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"no upstream":   {map[string]string{"HM_MCP_PUBLIC_URL": "https://mcp.example.com"}, "HM_MCP_UPSTREAM_URL"},
		"no public":     {map[string]string{"HM_MCP_UPSTREAM_URL": "https://hm.internal"}, "HM_MCP_PUBLIC_URL"},
		"http upstream": {map[string]string{"HM_MCP_UPSTREAM_URL": "http://hm.internal", "HM_MCP_PUBLIC_URL": "https://mcp.example.com"}, "HM_MCP_UPSTREAM_URL"},
		"bad max":       {map[string]string{"HM_MCP_UPSTREAM_URL": "https://hm.internal", "HM_MCP_PUBLIC_URL": "https://mcp.example.com", "HM_MCP_MAX_RESULT_BYTES": "5"}, "HM_MCP_MAX_RESULT_BYTES"},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr lockedBuffer
			code := run([]string{"serve"}, &stdout, &stderr, envOf(tc.env))
			if code != 2 {
				t.Errorf("exit code = %d, want 2", code)
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr %q does not name %s", stderr.String(), tc.want)
			}
		})
	}
}

func TestHealthcheck_ZeroWhenServingOneOtherwise(t *testing.T) {
	// Nothing listens: exit 1.
	addr := freeAddr(t)
	var stdout, stderr lockedBuffer
	if code := run([]string{"healthcheck"}, &stdout, &stderr, envOf(map[string]string{"HM_MCP_LISTEN": addr})); code != 1 {
		t.Errorf("not serving: exit code = %d, want 1", code)
	}

	env := map[string]string{"HM_MCP_UPSTREAM_URL": "http://127.0.0.1:1", "HM_MCP_PUBLIC_URL": "http://127.0.0.1:8481"}
	listen, stop := startServing(t, env)
	if code := run([]string{"healthcheck"}, &stdout, &stderr, envOf(map[string]string{"HM_MCP_LISTEN": listen})); code != 0 {
		t.Errorf("serving: exit code = %d, want 0 (%s)", code, stderr.String())
	}
	if code := stop(); code != 0 {
		t.Errorf("graceful shutdown exit code = %d, want 0", code)
	}
	if code := run([]string{"healthcheck"}, &stdout, &stderr, envOf(map[string]string{"HM_MCP_LISTEN": listen})); code != 1 {
		t.Errorf("after shutdown: exit code = %d, want 1", code)
	}
}

func TestHealthcheck_BadListenSettingIsOne(t *testing.T) {
	var stdout, stderr lockedBuffer
	if code := run([]string{"healthcheck"}, &stdout, &stderr, envOf(map[string]string{"HM_MCP_LISTEN": "nonsense"})); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestHealthz_DoesNotDependOnUpstream(t *testing.T) {
	// Nothing listens on the upstream port.
	env := map[string]string{"HM_MCP_UPSTREAM_URL": "http://" + freeAddr(t), "HM_MCP_PUBLIC_URL": "http://127.0.0.1:8481"}
	listen, _ := startServing(t, env)
	var stdout, stderr lockedBuffer
	if code := run([]string{"healthcheck"}, &stdout, &stderr, envOf(map[string]string{"HM_MCP_LISTEN": listen})); code != 0 {
		t.Errorf("exit code = %d, want 0 with the upstream down (%s)", code, stderr.String())
	}
}

func TestServe_LogsAStartLineWithoutSecrets(t *testing.T) {
	addr := freeAddr(t)
	env := map[string]string{"HM_MCP_UPSTREAM_URL": "http://127.0.0.1:1", "HM_MCP_PUBLIC_URL": "http://127.0.0.1:8481", "HM_MCP_LISTEN": addr}
	ctx, cancel := context.WithCancel(context.Background())
	stderr := &lockedBuffer{}
	done := make(chan int, 1)
	go func() { done <- serve(ctx, stderr, envOf(env)) }()
	for i := 0; i < 100 && !strings.Contains(stderr.String(), "starting"); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	log := stderr.String()
	for _, want := range []string{"harbormaster-mcp starting", "127.0.0.1:1", "http://127.0.0.1:8481"} {
		if !strings.Contains(log, want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
}

func TestVersion_Prints(t *testing.T) {
	var stdout, stderr lockedBuffer
	if code := run([]string{"version"}, &stdout, &stderr, envOf(nil)); code != 0 {
		t.Errorf("exit code = %d", code)
	}
	if got := stdout.String(); !strings.Contains(got, "harbormaster-mcp") || !strings.Contains(got, version) || !strings.Contains(got, commit) {
		t.Errorf("stdout = %q", got)
	}
}

func TestUsage(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		out  string // "stdout" or "stderr"
	}{
		{nil, 2, "stderr"}, {[]string{"bogus"}, 2, "stderr"}, {[]string{"-h"}, 0, "stdout"}, {[]string{"--help"}, 0, "stdout"},
	} {
		var stdout, stderr lockedBuffer
		code := run(tc.args, &stdout, &stderr, envOf(nil))
		if code != tc.code {
			t.Errorf("%v: exit code = %d, want %d", tc.args, code, tc.code)
		}
		text := stderr.String()
		if tc.out == "stdout" {
			text = stdout.String()
		}
		if !strings.Contains(text, "usage: harbormaster-mcp") {
			t.Errorf("%v: %s lacks usage text: %q", tc.args, tc.out, text)
		}
	}
}
