package execbridge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/coder/websocket"
)

func TestValidateStreamURL_AcceptsAmazonHosts(t *testing.T) {
	for _, u := range []string{
		"wss://ssmmessages.eu-west-1.amazonaws.com/v1/data-channel/x?role=publish_subscribe",
		"wss://ssmmessages.cn-north-1.amazonaws.com.cn/v1/data-channel/x",
		"wss://vpce-1.ssmmessages.us-east-1.vpce.amazonaws.com/v1/data-channel/x",
		"wss://SSMMESSAGES.us-east-1.AmazonAWS.com./x",
	} {
		if err := ValidateStreamURL(u, false); err != nil {
			t.Errorf("ValidateStreamURL(%q) = %v, want nil", u, err)
		}
	}
}

func TestValidateStreamURL_RejectsOthers(t *testing.T) {
	for _, u := range []string{
		"ws://ssmmessages.us-east-1.amazonaws.com/x",
		"wss://evil.example/x",
		"wss://user@ssmmessages.us-east-1.amazonaws.com/x",
		"wss://user:pw@ssmmessages.us-east-1.amazonaws.com/x",
		"wss://ssmmessages.us-east-1.amazonaws.com.evil/x",
		"wss://amazonaws.com/x",
		"wss://evilamazonaws.com/x",
		"https://ssmmessages.us-east-1.amazonaws.com/x",
		"wss:///x",
		"",
		"::not a url",
	} {
		if err := ValidateStreamURL(u, false); !errors.Is(err, ErrStreamURLNotAllowed) {
			t.Errorf("ValidateStreamURL(%q) = %v, want ErrStreamURLNotAllowed", u, err)
		}
	}
}

func TestValidateStreamURL_AllowInsecureLoopbackOnly(t *testing.T) {
	for _, u := range []string{"ws://127.0.0.1:9/x", "ws://localhost:9/x"} {
		if err := ValidateStreamURL(u, true); err != nil {
			t.Errorf("allowInsecure %q: %v", u, err)
		}
		if err := ValidateStreamURL(u, false); !errors.Is(err, ErrStreamURLNotAllowed) {
			t.Errorf("secure %q: %v, want ErrStreamURLNotAllowed", u, err)
		}
	}
	for _, u := range []string{"ws://10.0.0.1/x", "ws://127.0.0.1.evil.example/x", "ws://user@127.0.0.1:9/x", "ws://[::1]:9/x"} {
		if err := ValidateStreamURL(u, true); !errors.Is(err, ErrStreamURLNotAllowed) {
			t.Errorf("allowInsecure %q: %v, want ErrStreamURLNotAllowed", u, err)
		}
	}
}

// wsURL converts an httptest server URL into a ws:// URL.
func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

// echoServer accepts websockets and echoes every frame with its type.
func echoServer(t *testing.T, onRequest func(*http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if onRequest != nil {
			onRequest(r)
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, b); err != nil {
				return
			}
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestDial_SignsUpgradeWhenCredentialsGiven(t *testing.T) {
	var mu sync.Mutex
	var hdr http.Header
	s := echoServer(t, func(r *http.Request) { mu.Lock(); hdr = r.Header.Clone(); mu.Unlock() })
	conn, err := Dial(context.Background(), wsURL(s), DialOptions{
		Region:        "eu-west-1",
		Credentials:   credentials.NewStaticCredentialsProvider("AKID", "secret", ""),
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mu.Lock()
	defer mu.Unlock()
	auth := hdr.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKID/") || !strings.Contains(auth, "/eu-west-1/ssmmessages/aws4_request") {
		t.Errorf("Authorization = %q", auth)
	}
	if hdr.Get("X-Amz-Date") == "" {
		t.Error("X-Amz-Date missing")
	}
}

func TestDial_DoesNotSignPresignedURL(t *testing.T) {
	var mu sync.Mutex
	var auth string
	s := echoServer(t, func(r *http.Request) { mu.Lock(); auth = r.Header.Get("Authorization"); mu.Unlock() })
	conn, err := Dial(context.Background(), wsURL(s)+"/x?X-Amz-Signature=abc", DialOptions{
		Region:        "eu-west-1",
		Credentials:   credentials.NewStaticCredentialsProvider("AKID", "secret", ""),
		AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mu.Lock()
	defer mu.Unlock()
	if auth != "" {
		t.Errorf("presigned URL was signed: %q", auth)
	}
}

func TestDial_UnsignedWhenNoCredentials(t *testing.T) {
	var mu sync.Mutex
	var auth string
	s := echoServer(t, func(r *http.Request) { mu.Lock(); auth = r.Header.Get("Authorization"); mu.Unlock() })
	conn, err := Dial(context.Background(), wsURL(s), DialOptions{Region: "eu-west-1", AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	mu.Lock()
	defer mu.Unlock()
	if auth != "" {
		t.Errorf("Authorization = %q, want none", auth)
	}
}

func TestDial_DisallowedURLRejected(t *testing.T) {
	_, err := Dial(context.Background(), "wss://evil.example/x", DialOptions{})
	if !errors.Is(err, ErrStreamURLNotAllowed) {
		t.Errorf("err = %v, want ErrStreamURLNotAllowed", err)
	}
}

type failingCreds struct{}

func (failingCreds) Retrieve(context.Context) (aws.Credentials, error) {
	return aws.Credentials{}, errors.New("no creds today")
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestDial_SigningFailureIsLoggedNotFatal(t *testing.T) {
	var mu sync.Mutex
	var auth string
	s := echoServer(t, func(r *http.Request) { mu.Lock(); auth = r.Header.Get("Authorization"); mu.Unlock() })
	logs := &syncBuffer{}
	conn, err := Dial(context.Background(), wsURL(s), DialOptions{
		Region:        "eu-west-1",
		Credentials:   failingCreds{},
		AllowInsecure: true,
		Logger:        slog.New(slog.NewTextHandler(logs, nil)),
	})
	if err != nil {
		t.Fatalf("dial must proceed unsigned: %v", err)
	}
	defer conn.Close()
	if !strings.Contains(logs.String(), "sign") {
		t.Errorf("log %q does not mention signing", logs.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if auth != "" {
		t.Errorf("Authorization = %q, want none", auth)
	}
	if strings.Contains(logs.String(), "no creds today") == false {
		t.Errorf("log should include the error: %q", logs.String())
	}
}

func dialEcho(t *testing.T, o DialOptions) FrameConn {
	t.Helper()
	s := echoServer(t, nil)
	o.AllowInsecure = true
	c, err := Dial(context.Background(), wsURL(s), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestFrameConn_TextAndBinaryRoundTrip(t *testing.T) {
	c := dialEcho(t, DialOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.WriteText(ctx, []byte("a")); err != nil {
		t.Fatal(err)
	}
	text, data, err := c.ReadFrame(ctx)
	if err != nil || !text || string(data) != "a" {
		t.Fatalf("text frame: text=%v data=%q err=%v", text, data, err)
	}
	if err := c.WriteBinary(ctx, []byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	text, data, err = c.ReadFrame(ctx)
	if err != nil || text || !bytes.Equal(data, []byte{1, 2}) {
		t.Fatalf("binary frame: text=%v data=%v err=%v", text, data, err)
	}
}

func TestFrameConn_ConcurrentWritesAreSerialized(t *testing.T) {
	const n = 50
	c := dialEcho(t, DialOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := c.WriteBinary(ctx, []byte{byte(i)}); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	seen := map[byte]bool{}
	for i := 0; i < n; i++ {
		_, data, err := c.ReadFrame(ctx)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		seen[data[0]] = true
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("received %d distinct frames, want %d", len(seen), n)
	}
}

func TestFrameConn_ReadAfterServerClose_ReturnsError(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		c.Close(websocket.StatusNormalClosure, "bye")
	}))
	defer s.Close()
	c, err := Dial(context.Background(), wsURL(s), DialOptions{AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.ReadFrame(ctx); err == nil {
		t.Error("ReadFrame after server close returned nil error")
	}
	// Close must be idempotent; repeated calls must not panic or hang.
	c.Close()
	c.Close()
}

func TestFrameConn_ReadFrameHonoursContext(t *testing.T) {
	c := dialEcho(t, DialOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, _, err := c.ReadFrame(ctx); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error after ctx expiry")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ReadFrame did not return after ctx expiry")
	}
}

func TestDial_PingKeepalive(t *testing.T) {
	var pings atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		// Reading drives the control-frame handling (ping -> pong).
		c.SetReadLimit(1 << 10)
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer s.Close()

	// coder/websocket exposes no server-side ping callback, so the client
	// reports each completed ping/pong round trip through a test hook.
	c, err := Dial(context.Background(), wsURL(s), DialOptions{
		AllowInsecure: true,
		PingInterval:  20 * time.Millisecond,
		onPingSent:    func() { pings.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// ReadFrame must be active for pongs to be processed by the client.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { c.ReadFrame(ctx) }()
	defer cancel()

	deadline := time.Now().Add(2 * time.Second)
	for pings.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pings.Load() < 1 {
		t.Error("no ping observed")
	}
}

func TestDial_PingKeepaliveSurvivesMissedPong(t *testing.T) {
	var pings atomic.Int32
	startReading := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		<-startReading // not reading: pongs cannot be produced
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer s.Close()

	c, err := Dial(context.Background(), wsURL(s), DialOptions{
		AllowInsecure: true,
		PingInterval:  20 * time.Millisecond,
		pongTimeout:   10 * time.Millisecond,
		onPingSent:    func() { pings.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { c.ReadFrame(ctx) }()

	// Let several pings time out, then let the server answer again.
	time.Sleep(150 * time.Millisecond)
	if n := pings.Load(); n != 0 {
		t.Fatalf("pings completed while server was not reading: %d", n)
	}
	close(startReading)
	deadline := time.Now().Add(2 * time.Second)
	for pings.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if pings.Load() < 1 {
		t.Error("keepalive did not resume after missed pongs")
	}
}
