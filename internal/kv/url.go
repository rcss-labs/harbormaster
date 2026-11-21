package kv

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Options configure Open. Address is required; Prefix defaults to "hm" and ClientName to "harbormaster".
type Options struct {
	Address            string // host:port
	Username, Password string
	DB                 int
	TLS                bool // system roots
	Prefix, ClientName string
	// OnCommand, when set, is told the allow-list name of every command the client issues. Tests use it.
	OnCommand func(name string)
}

// ParseURL reads a redis, rediss, valkey or valkeys URL. The URL can carry a password, so no error names the
// input or any part of it.
func ParseURL(raw string) (Options, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Options{}, errors.New("kv url: not a valid URL")
	}
	var o Options
	switch strings.ToLower(u.Scheme) {
	case "redis", "valkey":
	case "rediss", "valkeys":
		o.TLS = true
	default:
		return Options{}, errors.New("kv url: scheme must be redis, rediss, valkey or valkeys")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, " \t") {
		return Options{}, errors.New("kv url: address is missing or invalid")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	} else if n, perr := strconv.Atoi(port); perr != nil || n < 1 || n > 65535 {
		return Options{}, errors.New("kv url: port must be a number from 1 to 65535")
	}
	o.Address = net.JoinHostPort(host, port)
	if u.User != nil {
		o.Username = u.User.Username()
		o.Password, _ = u.User.Password()
	}
	if path := strings.TrimPrefix(u.Path, "/"); path != "" {
		n, perr := strconv.Atoi(path)
		if perr != nil || n < 0 {
			return Options{}, errors.New("kv url: the path must be a database number, 0 or more")
		}
		o.DB = n
	}
	if u.RawQuery != "" {
		if _, qerr := url.ParseQuery(u.RawQuery); qerr != nil {
			return Options{}, errors.New("kv url: query is not valid")
		}
	}
	return o, nil
}

// scrubbed hides the URL and password in the message but keeps the cause reachable for errors.Is and As.
type scrubbed struct {
	msg   string
	cause error
}

func (e *scrubbed) Error() string { return e.msg }
func (e *scrubbed) Unwrap() error { return e.cause }

// Scrub returns err with the URL and its password replaced in the message. Use it on any error that came from
// opening or talking to the store before it is logged or returned.
func Scrub(err error, rawURL string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	var secrets []string
	if u, perr := url.Parse(rawURL); perr == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok && pw != "" {
			secrets = append(secrets, pw, url.QueryEscape(pw))
		}
	}
	if rawURL != "" {
		msg = strings.ReplaceAll(msg, rawURL, "[kv url]")
	}
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "[redacted]")
	}
	return &scrubbed{msg: fmt.Sprint(msg), cause: err}
}
