package notify

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func smtpEnabled() config.SMTPConfig {
	return config.SMTPConfig{Host: "mail.example.com", Port: 587, From: "Harbormaster <hm@example.com>", TLS: "starttls"}
}

// fakeSMTP is a minimal loopback SMTP server: EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT, DATA, QUIT.
type fakeSMTP struct {
	listener  net.Listener
	tlsConfig *tls.Config
	clientTLS *tls.Config
	port      int

	offerStartTLS bool
	implicitTLS   bool
	rcptReply     string // reply to RCPT, "" = 250 ok

	mu         sync.Mutex
	auth       string
	mailFrom   string
	recipients []string
	data       string
}

func newFakeSMTP(t *testing.T, offerStartTLS, implicitTLS bool) *fakeSMTP {
	t.Helper()
	certSource := httptest.NewTLSServer(http.NotFoundHandler()) // only for its certificate
	t.Cleanup(certSource.Close)
	pool := x509.NewCertPool()
	pool.AddCert(certSource.Certificate())
	f := &fakeSMTP{
		tlsConfig:     &tls.Config{Certificates: certSource.TLS.Certificates},
		clientTLS:     &tls.Config{RootCAs: pool, ServerName: "example.com"},
		offerStartTLS: offerStartTLS, implicitTLS: implicitTLS,
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.listener = listener
	f.port = listener.Addr().(*net.TCPAddr).Port
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(conn net.Conn) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if f.implicitTLS {
		conn = tls.Server(conn, f.tlsConfig)
	}
	reader := bufio.NewReader(conn)
	reply := func(s string) { conn.Write([]byte(s + "\r\n")) }
	reply("220 localhost ESMTP")
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "EHLO"):
			lines := []string{"250-localhost", "250-AUTH PLAIN"}
			if f.offerStartTLS {
				lines = append(lines, "250-STARTTLS")
			}
			lines = append(lines, "250 8BITMIME")
			reply(strings.Join(lines, "\r\n"))
		case upper == "STARTTLS":
			reply("220 Ready to start TLS")
			conn = tls.Server(conn, f.tlsConfig)
			reader = bufio.NewReader(conn)
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			f.mu.Lock()
			f.auth = strings.ReplaceAll(string(raw), "\x00", "|")
			f.mu.Unlock()
			reply("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			f.mu.Lock()
			f.mailFrom = line[len("MAIL FROM:"):]
			f.mu.Unlock()
			reply("250 ok")
		case strings.HasPrefix(upper, "RCPT TO:"):
			if f.rcptReply != "" {
				reply(f.rcptReply)
				continue
			}
			f.mu.Lock()
			f.recipients = append(f.recipients, line[len("RCPT TO:"):])
			f.mu.Unlock()
			reply("250 ok")
		case upper == "DATA":
			reply("354 go ahead")
			var data strings.Builder
			for {
				l, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				data.WriteString(l)
			}
			f.mu.Lock()
			f.data = data.String()
			f.mu.Unlock()
			reply("250 queued")
		case upper == "QUIT":
			reply("221 bye")
			return
		default:
			reply("502 not implemented")
		}
	}
}

func (f *fakeSMTP) senders(tlsMode string) *Senders {
	return &Senders{
		SMTP:    config.SMTPConfig{Host: "127.0.0.1", Port: f.port, Username: "mailer", Password: "mailpass", From: "Harbormaster <hm@example.com>", TLS: tlsMode},
		Timeout: 3 * time.Second, tls: f.clientTLS,
	}
}

func emailRoute() store.NotificationRoute {
	return store.NotificationRoute{Destination: DestEmail, Config: store.NotificationConfig{Recipients: []string{"ops@example.com", "dev@example.com"}}}
}

func TestSMTPSender_StartTLSAuthAndMessage(t *testing.T) {
	f := newFakeSMTP(t, true, false)
	res := f.senders("starttls").Send(context.Background(), emailRoute(), Secret{}, testMessage(EventOpened))
	if !res.Delivered || !strings.HasPrefix(res.Note, "Delivered in ") {
		t.Fatalf("result = %+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "|mailer|mailpass" {
		t.Errorf("auth = %q, want PLAIN credentials", f.auth)
	}
	if !strings.Contains(f.mailFrom, "hm@example.com") {
		t.Errorf("MAIL FROM = %q", f.mailFrom)
	}
	if len(f.recipients) != 2 || !strings.Contains(f.recipients[0], "ops@example.com") || !strings.Contains(f.recipients[1], "dev@example.com") {
		t.Errorf("recipients = %v, want both", f.recipients)
	}
	for _, want := range []string{"Subject: [Harbormaster] api: service failing", "To: ops@example.com, dev@example.com", "0 of 3 tasks are running."} {
		if !strings.Contains(f.data, want) {
			t.Errorf("message lacks %q:\n%s", want, f.data)
		}
	}
}

func TestSMTPSender_ImplicitTLS(t *testing.T) {
	f := newFakeSMTP(t, false, true)
	if res := f.senders("implicit").Send(context.Background(), emailRoute(), Secret{}, testMessage(EventOpened)); !res.Delivered {
		t.Fatalf("result = %+v", res)
	}
}

func TestSMTPSender_RefusesWithoutStartTLS(t *testing.T) {
	f := newFakeSMTP(t, false, false)
	res := f.senders("starttls").Send(context.Background(), emailRoute(), Secret{}, testMessage(EventOpened))
	if res.Delivered || res.Retryable || res.Note != "The mail server does not offer STARTTLS." {
		t.Fatalf("result = %+v", res)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.auth != "" || f.data != "" {
		t.Error("credentials or mail were sent over an unencrypted connection")
	}
}

func TestSMTPSender_ReplyCodeMapping(t *testing.T) {
	for reply, retryable := range map[string]bool{"451 4.3.0 try again later": true, "550 5.1.1 no such user": false} {
		f := newFakeSMTP(t, true, false)
		f.rcptReply = reply
		res := f.senders("starttls").Send(context.Background(), emailRoute(), Secret{}, testMessage(EventOpened))
		code := reply[:3]
		if res.Delivered || res.Retryable != retryable || res.Note != "SMTP "+code {
			t.Errorf("reply %q: result = %+v", reply, res)
		}
		if _, err := strconv.Atoi(code); err != nil || strings.Contains(res.Note, "no such user") {
			t.Errorf("the note must carry the code only: %q", res.Note)
		}
	}
}

func TestSMTPSender_ConnectionFailureIsRetryable(t *testing.T) {
	f := newFakeSMTP(t, true, false)
	s := f.senders("starttls")
	f.listener.Close()
	res := s.Send(context.Background(), emailRoute(), Secret{}, testMessage(EventOpened))
	if res.Delivered || !res.Retryable || res.Note != "Network error" {
		t.Fatalf("result = %+v", res)
	}
}

func TestSMTPSender_FakeModeRecordsInsteadOfSending(t *testing.T) {
	var gotFrom string
	var gotTo []string
	var gotMsg []byte
	s := &Senders{SMTP: config.SMTPConfig{From: "hm@example.com"}, FakeMail: func(from string, to []string, msg []byte) error {
		gotFrom, gotTo, gotMsg = from, to, msg
		return nil
	}}
	res := s.Send(context.Background(), emailRoute(), Secret{}, testMessage(EventOpened))
	if !res.Delivered || gotFrom != "hm@example.com" || len(gotTo) != 2 || !strings.Contains(string(gotMsg), "Subject: [Harbormaster]") {
		t.Fatalf("result = %+v from=%q to=%v", res, gotFrom, gotTo)
	}
}
