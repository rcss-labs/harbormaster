package scanjob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

const maxRedirects = 5

// HTTPDBSource reads Anchore's DB listing and archives over https. Only https URLs are used and a redirect may not leave
// the host it started on. Whether Anchore serves archives from the same host is unverified, so the final URL of the first
// request is logged.
type HTTPDBSource struct {
	// Log receives the final URL of the first request; nil logs nothing.
	Log *slog.Logger

	client    *http.Client
	latestURL string
	logOnce   sync.Once
}

// NewHTTPDBSource reads latestURL and archives that sit beside it. A nil client means http.DefaultClient's settings.
func NewHTTPDBSource(client *http.Client, latestURL string) *HTTPDBSource {
	copied := http.Client{}
	if client != nil {
		copied = *client
	}
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		switch {
		case len(via) >= maxRedirects:
			return errors.New("too many redirects")
		case req.URL.Scheme != "https":
			return errors.New("redirect to a non-https URL refused")
		case req.URL.Host != via[0].URL.Host:
			return fmt.Errorf("redirect to another host (%s) refused", req.URL.Host)
		}
		return nil
	}
	return &HTTPDBSource{client: &copied, latestURL: latestURL}
}

var _ DBSource = (*HTTPDBSource)(nil)

func (s *HTTPDBSource) get(ctx context.Context, rawURL string) (*http.Response, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("the vulnerability data URL must be https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	s.logOnce.Do(func() {
		if s.Log != nil {
			s.Log.Info("scan db source: first request done", "final_url", resp.Request.URL.String())
		}
	})
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("vulnerability data server answered %s", resp.Status)
	}
	return resp, nil
}

// FetchLatest returns Anchore's latest.json, at most scan.MaxDBLatestBytes.
func (s *HTTPDBSource) FetchLatest(ctx context.Context) ([]byte, error) {
	resp, err := s.get(ctx, s.latestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, scan.MaxDBLatestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > scan.MaxDBLatestBytes {
		return nil, errors.New("upstream db listing is larger than the allowed size")
	}
	return data, nil
}

// OpenArchive streams the archive with the given bare file name from beside latest.json. The caller caps the stream.
func (s *HTTPDBSource) OpenArchive(ctx context.Context, name string) (io.ReadCloser, error) {
	if name == "" || strings.ContainsAny(name, `/\?#`) {
		return nil, errors.New("archive name must be a bare file name")
	}
	base, err := url.Parse(s.latestURL)
	if err != nil {
		return nil, err
	}
	archive := url.URL{Scheme: base.Scheme, Host: base.Host, Path: path.Join(path.Dir(base.Path), name)}
	resp, err := s.get(ctx, archive.String())
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}
