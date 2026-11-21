// Package kvtest gives tests a kv.Client per server listed in HM_TEST_KV_URLS (see "make test-kv"). It does not
// import the client library: the admin connection speaks plain RESP so that internal/kv/valkey.go stays the only
// importer.
package kvtest

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

// EnvVar names the comma-separated list of test server URLs.
const EnvVar = "HM_TEST_KV_URLS"

// URLs returns the servers to test against, or nil when the variable is empty.
func URLs() []string {
	var out []string
	for _, u := range strings.Split(os.Getenv(EnvVar), ",") {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}

// Each runs fn as a subtest per server, or skips when no server is configured.
func Each(t *testing.T, fn func(t *testing.T, c kv.Client)) {
	t.Helper()
	urls := URLs()
	if len(urls) == 0 {
		t.Skip(EnvVar + " not set")
	}
	for _, u := range urls {
		u := u
		t.Run(label(u), func(t *testing.T) {
			fn(t, Open(t, u))
		})
	}
}

// label names a subtest after the port, which tells the engines apart without printing credentials.
func label(rawURL string) string {
	o, err := kv.ParseURL(rawURL)
	if err != nil {
		return "bad-url"
	}
	_, port, _ := net.SplitHostPort(o.Address)
	return "port" + port
}

// Open returns a client under a unique prefix, closed when the test ends. The cleanup fails the test if the
// client sent a command outside kv.AllowedCommands.
func Open(t testing.TB, rawURL string) kv.Client {
	t.Helper()
	o, err := kv.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("kvtest: %v", err)
	}
	o.Prefix = "t" + randomHex(4)
	rec := &recorder{}
	o.OnCommand = rec.add
	c, err := kv.Open(o)
	if err != nil {
		t.Fatalf("kvtest: open: %v", kv.Scrub(err, rawURL))
	}
	t.Cleanup(func() {
		c.Close()
		for _, name := range rec.names() {
			if !kv.AllowedCommands[name] {
				t.Errorf("kvtest: the client sent %s, which is not in kv.AllowedCommands", name)
			}
		}
	})
	return c
}

type recorder struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (r *recorder) add(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[string]bool{}
	}
	r.seen[name] = true
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.seen))
	for n := range r.seen {
		out = append(out, n)
	}
	return out
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Admin is a raw connection for test tooling that the client must never send (CLIENT KILL, SCRIPT FLUSH,
// INFO commandstats). It speaks RESP2 and is not subject to the allow-list.
type Admin struct {
	conn net.Conn
	rd   *bufio.Reader
}

// OpenAdmin connects, authenticates when the URL has credentials, and closes on cleanup.
func OpenAdmin(t testing.TB, rawURL string) *Admin {
	t.Helper()
	o, err := kv.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("kvtest: %v", err)
	}
	conn, err := net.DialTimeout("tcp", o.Address, 2*time.Second)
	if err != nil {
		t.Fatalf("kvtest: admin dial: %v", err)
	}
	a := &Admin{conn: conn, rd: bufio.NewReader(conn)}
	t.Cleanup(func() { _ = conn.Close() })
	if o.Password != "" {
		args := []string{"AUTH"}
		if o.Username != "" {
			args = append(args, o.Username)
		}
		if _, err := a.Do(append(args, o.Password)...); err != nil {
			t.Fatalf("kvtest: admin auth failed")
		}
	}
	return a
}

// Do sends one command and returns the reply flattened: simple strings and bulk strings as they are, integers in
// decimal, arrays joined with newlines. An error reply becomes an error.
func (a *Admin) Do(args ...string) (string, error) {
	_ = a.conn.SetDeadline(time.Now().Add(5 * time.Second))
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, s := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(s), s)
	}
	if _, err := io.WriteString(a.conn, b.String()); err != nil {
		return "", err
	}
	return a.read()
}

func (a *Admin) read() (string, error) {
	line, err := a.rd.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", fmt.Errorf("empty reply")
	}
	body := line[1:]
	switch line[0] {
	case '+', ':':
		return body, nil
	case '-':
		return "", fmt.Errorf("server error: %s", body)
	case '$':
		n, _ := strconv.Atoi(body)
		if n < 0 {
			return "", nil
		}
		buf := make([]byte, n+2)
		if _, err := io.ReadFull(a.rd, buf); err != nil {
			return "", err
		}
		return string(buf[:n]), nil
	case '*':
		n, _ := strconv.Atoi(body)
		parts := make([]string, 0, max(n, 0))
		for i := 0; i < n; i++ {
			s, err := a.read()
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, "\n"), nil
	}
	return "", fmt.Errorf("unexpected reply type %q", line[0])
}

// DropPubSub kills the server's pub/sub connections so a subscriber has to reconnect.
func DropPubSub(t testing.TB, rawURL string) {
	t.Helper()
	a := OpenAdmin(t, rawURL)
	if _, err := a.Do("CLIENT", "KILL", "TYPE", "pubsub"); err != nil {
		t.Fatalf("kvtest: CLIENT KILL: %v", err)
	}
}
