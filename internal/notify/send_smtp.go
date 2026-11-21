package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// sendEmail sends one message to every recipient of the route. The SMTP host is deployer configuration, so the
// dial guard does not apply to it.
func (s *Senders) sendEmail(ctx context.Context, r store.NotificationRoute, _ Secret, m Message) Result {
	_, msg := Email(m, s.SMTP.From, r.Config.Recipients)
	start := time.Now()
	delivered := func() Result {
		elapsed := time.Since(start)
		return Result{Delivered: true, Duration: elapsed, Note: fmt.Sprintf("Delivered in %d ms", elapsed.Milliseconds())}
	}
	if s.FakeMail != nil {
		if err := s.FakeMail(s.SMTP.From, r.Config.Recipients, msg); err != nil {
			return Result{Retryable: true, Note: "Network error"}
		}
		return delivered()
	}
	if err := s.smtpExchange(ctx, r.Config.Recipients, msg); err != nil {
		return s.smtpResult(err)
	}
	return delivered()
}

var errNoStartTLS = errors.New("server does not offer STARTTLS")

func (s *Senders) smtpExchange(ctx context.Context, to []string, msg []byte) error {
	from, err := mail.ParseAddress(s.SMTP.From)
	if err != nil {
		return err
	}
	host := s.SMTP.Host
	address := net.JoinHostPort(host, strconv.Itoa(s.SMTP.Port))
	dialer := &net.Dialer{Timeout: s.timeout()}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	// One deadline for the whole exchange.
	if err := conn.SetDeadline(time.Now().Add(s.timeout())); err != nil {
		return err
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if s.tls != nil {
		tlsConfig = s.tls.Clone()
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = host
		}
	}
	if s.SMTP.TLS == "implicit" {
		conn = tls.Client(conn, tlsConfig)
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer client.Close()
	if s.SMTP.TLS != "implicit" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errNoStartTLS
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if s.SMTP.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.SMTP.Username, s.SMTP.Password, host)); err != nil {
			return err
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return err
	}
	for _, recipient := range to {
		address, err := mail.ParseAddress(recipient)
		if err != nil {
			return err
		}
		if err := client.Rcpt(address.Address); err != nil {
			return err
		}
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(msg); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// smtpResult maps an SMTP failure to a Result. 4xx replies are retryable, 5xx are not; the note is the code only,
// never the server's text (which can echo addresses).
func (s *Senders) smtpResult(err error) Result {
	if errors.Is(err, errNoStartTLS) {
		return Result{Note: "The mail server does not offer STARTTLS."}
	}
	var protocol *textproto.Error
	if errors.As(err, &protocol) {
		if s.Log != nil {
			s.Log.Warn("notification send failed", "class", "smtp", "code", protocol.Code)
		}
		return Result{Retryable: protocol.Code >= 400 && protocol.Code < 500, Note: fmt.Sprintf("SMTP %d", protocol.Code)}
	}
	return s.transportResult(err)
}
