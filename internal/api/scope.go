package api

import (
	"context"
	"encoding/base64"
	"net"
	"net/http"
	"slices"
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
	// TagFilter is the ?tag= selection (nil = no tag filter); it only ever narrows Vis.
	TagFilter []authz.TagID
}

// tagMatches reports whether a service/task/cluster with this effective tag passes the ?tag= filter.
func (sc scope) tagMatches(tag authz.TagID) bool {
	return sc.TagFilter == nil || slices.Contains(sc.TagFilter, tag)
}

// parseTagFilter reads the repeatable ?tag= values into a de-duplicated list; nil when none were given.
func parseTagFilter(values []string) ([]authz.TagID, error) {
	var out []authz.TagID
	for _, raw := range values {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, invalid("tag must be a number.")
		}
		if !slices.Contains(out, authz.TagID(id)) {
			out = append(out, authz.TagID(id))
		}
	}
	return out, nil
}

// narrowVisibility restricts vis to the selected tags. An admin becomes a tag-limited viewer of exactly
// those tags; anyone else keeps only the selected tags they may already see, so the filter never widens access.
func narrowVisibility(vis store.Visibility, tags []authz.TagID) store.Visibility {
	if len(tags) == 0 {
		return vis
	}
	narrowed := make([]int64, 0, len(tags))
	for _, tag := range tags {
		if vis.Admin || slices.Contains(vis.TagIDs, int64(tag)) {
			narrowed = append(narrowed, int64(tag))
		}
	}
	return store.Visibility{Admin: false, TagIDs: narrowed}
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
	tagFilter, err := parseTagFilter(q["tag"])
	if err != nil {
		return scope{}, err
	}
	return scope{
		Subject:   subject,
		Vis:       narrowVisibility(visibilityOf(subject), tagFilter),
		Filter:    store.TargetFilter{AccountID: q.Get("account"), Region: q.Get("region")},
		Tags:      tags,
		TagFilter: tagFilter,
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

// clientIP is the peer address, unless Cfg.TrustedProxy says exactly one trusted proxy (the ALB)
// appends the real client IP, in which case it is the rightmost non-empty X-Forwarded-For entry.
// Earlier entries are client-controlled and must not be trusted; without a trusted proxy the
// whole header is.
func (s *Server) clientIP(r *http.Request) string {
	if s.Cfg.TrustedProxy {
		hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			if hop := strings.TrimSpace(hops[i]); hop != "" {
				return hop
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
