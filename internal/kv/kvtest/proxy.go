package kvtest

import (
	"io"
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/kv"
)

// Proxy puts a TCP proxy in front of the server at rawURL so a test can cut and restore the connection. It
// returns the URL to give the client; cut closes the listener and every open connection, restore listens again on
// the same port. The proxy closes with the test.
func Proxy(t testing.TB, rawURL string) (proxiedURL string, cut func(), restore func()) {
	t.Helper()
	o, err := kv.ParseURL(rawURL)
	if err != nil {
		t.Fatalf("kvtest: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("kvtest: proxy listen: %v", err)
	}
	listenAddr := ln.Addr().String()

	var mu sync.Mutex
	var conns []net.Conn
	closed := false
	current := ln

	accept := func(l net.Listener) {
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			upstream, err := net.DialTimeout("tcp", o.Address, 2*time.Second)
			if err != nil {
				_ = client.Close()
				continue
			}
			mu.Lock()
			conns = append(conns, client, upstream)
			mu.Unlock()
			go func() { _, _ = io.Copy(upstream, client); _ = upstream.Close() }()
			go func() { _, _ = io.Copy(client, upstream); _ = client.Close() }()
		}
	}
	go accept(ln)

	closeAll := func() {
		mu.Lock()
		defer mu.Unlock()
		_ = current.Close()
		for _, c := range conns {
			_ = c.Close()
		}
		conns = nil
	}
	cut = closeAll
	restore = func() {
		mu.Lock()
		done := closed
		mu.Unlock()
		if done {
			return
		}
		var l net.Listener
		var err error
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if l, err = net.Listen("tcp", listenAddr); err == nil {
				break
			}
		}
		if err != nil {
			t.Errorf("kvtest: proxy relisten: %v", err)
			return
		}
		mu.Lock()
		current = l
		mu.Unlock()
		go accept(l)
	}
	t.Cleanup(func() {
		mu.Lock()
		closed = true
		mu.Unlock()
		closeAll()
	})

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal("kvtest: bad URL")
	}
	u.Host = listenAddr
	return u.String(), cut, restore
}
