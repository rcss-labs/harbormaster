package execbridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/coder/websocket"
)

// ErrStreamURLNotAllowed is returned when a stream URL fails ValidateStreamURL.
var ErrStreamURLNotAllowed = errors.New("execbridge: stream URL not allowed")

const (
	// emptyBodySHA256 is the SigV4 payload hash of an empty body (the upgrade GET).
	emptyBodySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	signingService  = "ssmmessages"
	// wsReadLimit bounds a single frame; data-channel messages are far smaller.
	wsReadLimit = 4 << 20
	// defaultPingInterval matches the SSM agent's keepalive cadence.
	defaultPingInterval = 5 * time.Minute
	pingTimeout         = 30 * time.Second
)

// DialOptions configures Dial.
type DialOptions struct {
	// Region is the SigV4 signing region.
	Region string
	// Credentials, when non-nil, are used to best-effort sign the upgrade request.
	Credentials aws.CredentialsProvider
	// AllowInsecure additionally permits ws:// to 127.0.0.1 and localhost (fake server).
	AllowInsecure bool
	// PingInterval defaults to 5 minutes.
	PingInterval time.Duration
	HTTPClient   *http.Client
	Logger       *slog.Logger

	// onPingSent is a test hook called after each completed ping/pong round trip.
	onPingSent func()
	// pongTimeout overrides the default pong wait; tests only.
	pongTimeout time.Duration
}

// ValidateStreamURL checks that raw is a websocket URL the bridge may dial: wss
// to an Amazon host without userinfo. With allowInsecure, ws:// to the explicit
// loopback hosts 127.0.0.1 and localhost is also accepted.
func ValidateStreamURL(raw string, allowInsecure bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: unparsable", ErrStreamURLNotAllowed)
	}
	if u.User != nil {
		return fmt.Errorf("%w: userinfo present", ErrStreamURLNotAllowed)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrStreamURLNotAllowed)
	}
	switch u.Scheme {
	case "wss":
		if strings.HasSuffix(host, ".amazonaws.com") || strings.HasSuffix(host, ".amazonaws.com.cn") {
			return nil
		}
		return fmt.Errorf("%w: host %q is not an Amazon endpoint", ErrStreamURLNotAllowed, host)
	case "ws":
		if allowInsecure && (host == "127.0.0.1" || host == "localhost") {
			return nil
		}
		return fmt.Errorf("%w: insecure scheme", ErrStreamURLNotAllowed)
	default:
		return fmt.Errorf("%w: scheme %q", ErrStreamURLNotAllowed, u.Scheme)
	}
}

// Dial opens the websocket to streamURL. The upgrade request is SigV4-signed
// for ssmmessages when credentials are supplied and the URL is not presigned;
// a signing failure is logged and the dial proceeds unsigned (the token in
// OpenDataChannelInput is what authorizes the channel).
func Dial(ctx context.Context, streamURL string, o DialOptions) (FrameConn, error) {
	if err := ValidateStreamURL(streamURL, o.AllowInsecure); err != nil {
		return nil, err
	}
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}

	header := http.Header{}
	if o.Credentials != nil && !isPresigned(streamURL) {
		if signed, err := signUpgrade(ctx, streamURL, o); err != nil {
			logger.Warn("execbridge: could not sign websocket upgrade; dialing unsigned", "error", err)
		} else {
			header = signed
		}
	}

	ws, _, err := websocket.Dial(ctx, streamURL, &websocket.DialOptions{
		HTTPHeader: header,
		HTTPClient: o.HTTPClient,
	})
	if err != nil {
		return nil, fmt.Errorf("execbridge: dial stream: %w", err)
	}
	ws.SetReadLimit(wsReadLimit)

	interval := o.PingInterval
	if interval <= 0 {
		interval = defaultPingInterval
	}
	pingCtx, stopPing := context.WithCancel(context.Background())
	pongWait := o.pongTimeout
	if pongWait <= 0 {
		pongWait = pingTimeout
	}
	c := &wsFrameConn{ws: ws, stopPing: stopPing, pingDone: make(chan struct{})}
	go c.pingLoop(pingCtx, interval, pongWait, logger, o.onPingSent)
	return c, nil
}

func isPresigned(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for k := range u.Query() {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") {
			return true
		}
	}
	return false
}

// signUpgrade returns the headers SigV4 adds to the upgrade GET.
func signUpgrade(ctx context.Context, streamURL string, o DialOptions) (http.Header, error) {
	creds, err := o.Credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("sign: retrieve credentials: %w", err)
	}
	u, err := url.Parse(streamURL)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	// The canonical request does not include the scheme; sign as HTTP(S) GET.
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	if err := v4.NewSigner().SignHTTP(ctx, creds, req, emptyBodySHA256, signingService, o.Region, time.Now()); err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return req.Header.Clone(), nil
}

// wsFrameConn adapts a coder/websocket connection to FrameConn.
type wsFrameConn struct {
	ws       *websocket.Conn
	stopPing context.CancelFunc
	pingDone chan struct{}
	closeOne sync.Once
	closeErr error
}

func (c *wsFrameConn) ReadFrame(ctx context.Context) (bool, []byte, error) {
	typ, data, err := c.ws.Read(ctx)
	if err != nil {
		return false, nil, err
	}
	return typ == websocket.MessageText, data, nil
}

func (c *wsFrameConn) WriteBinary(ctx context.Context, b []byte) error {
	return c.write(ctx, websocket.MessageBinary, b)
}

func (c *wsFrameConn) WriteText(ctx context.Context, b []byte) error {
	return c.write(ctx, websocket.MessageText, b)
}

func (c *wsFrameConn) write(ctx context.Context, typ websocket.MessageType, b []byte) error {
	// coder/websocket serialises concurrent writes itself and honours ctx
	// while waiting for its internal lock.
	return c.ws.Write(ctx, typ, b)
}

// Close stops the keepalive and closes the connection. It is idempotent.
func (c *wsFrameConn) Close() error {
	c.closeOne.Do(func() {
		c.stopPing()
		c.closeErr = c.ws.CloseNow()
		<-c.pingDone
	})
	return c.closeErr
}

// pingLoop sends a keepalive ping every interval. Like the SSM agent it does
// not require pongs: a pong only arrives while a Read is running, so a missed
// pong is logged and the loop carries on. It exits when ctx is cancelled or the
// connection is closed.
func (c *wsFrameConn) pingLoop(ctx context.Context, interval, pongWait time.Duration, logger *slog.Logger, onPing func()) {
	defer close(c.pingDone)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, pongWait)
			err := c.ws.Ping(pctx)
			cancel()
			switch {
			case err == nil:
				if onPing != nil {
					onPing()
				}
			case ctx.Err() != nil:
				return
			case errors.Is(err, context.DeadlineExceeded):
				logger.Debug("execbridge: no pong for keepalive ping; continuing", "error", err)
			default:
				return // connection closed; the reader will see it
			}
		}
	}
}
