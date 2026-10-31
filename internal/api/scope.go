package api

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// scope is what a read handler needs to filter by: who is asking and which account/region they narrowed to.
type scope struct {
	Subject authz.Subject
	Vis     store.Visibility
	Filter  store.TargetFilter
	Tags    authz.Tags
}

// visibilityOf converts a subject to the store's visibility filter (never a nil tag list).
func visibilityOf(subject authz.Subject) store.Visibility {
	ids := make([]int64, 0, len(subject.Perms))
	for _, id := range authz.VisibleTags(subject) {
		ids = append(ids, int64(id))
	}
	return store.Visibility{Admin: subject.Admin, TagIDs: ids}
}

// scopeFrom builds the request scope from the authenticated subject and ?account=&region=.
func (s *Server) scopeFrom(r *http.Request) (scope, error) {
	tags, err := s.tags(r)
	if err != nil {
		return scope{}, err
	}
	subject := auth.SubjectFrom(r.Context())
	q := r.URL.Query()
	return scope{
		Subject: subject,
		Vis:     visibilityOf(subject),
		Filter:  store.TargetFilter{AccountID: q.Get("account"), Region: q.Get("region")},
		Tags:    tags,
	}, nil
}

type tagsMemoKey struct{}

// tagsMemo caches the tag snapshot for one request; installed by withTagsMemo.
type tagsMemo struct {
	once sync.Once
	tags authz.Tags
	err  error
}

func withTagsMemo(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tagsMemoKey{}, &tagsMemo{})))
	})
}

// tags returns the per-request tag snapshot, loading it from the store at most once.
func (s *Server) tags(r *http.Request) (authz.Tags, error) {
	memo, ok := r.Context().Value(tagsMemoKey{}).(*tagsMemo)
	if !ok {
		return s.Store.TagsSnapshot(r.Context())
	}
	memo.once.Do(func() { memo.tags, memo.err = s.Store.TagsSnapshot(r.Context()) })
	return memo.tags, memo.err
}

// limitFrom reads ?limit= (default and cap from cfg).
func limitFrom(r *http.Request, cfg config.Config) (int, error) {
	limit := cfg.PageSizeDefault
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 {
			return 0, invalid("limit must be a positive number.")
		}
	}
	return min(limit, cfg.PageSizeMax), nil
}

// pageFrom reads ?limit= (default and cap from cfg) and ?cursor= (base64url of the decimal offset).
func pageFrom(r *http.Request, cfg config.Config) (limit, offset int, err error) {
	if limit, err = limitFrom(r, cfg); err != nil {
		return 0, 0, err
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, derr := base64.RawURLEncoding.DecodeString(raw)
		if derr == nil {
			offset, derr = strconv.Atoi(string(decoded))
		}
		if derr != nil || offset < 0 {
			return 0, 0, invalid("cursor is not valid.")
		}
	}
	return limit, offset, nil
}

// nextCursor returns the cursor for the page after one that started at offset and returned
// returned items out of limit; nil when the page was not full (no more data).
func nextCursor(limit, offset, returned int) *string {
	if returned < limit {
		return nil
	}
	c := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset + returned)))
	return &c
}

// clientIP is the rightmost non-empty X-Forwarded-For entry, else the peer address. It assumes
// exactly one trusted proxy (the ALB), which appends the real client IP; earlier entries are
// client-controlled and must not be trusted.
func clientIP(r *http.Request) string {
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		if hop := strings.TrimSpace(hops[i]); hop != "" {
			return hop
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
