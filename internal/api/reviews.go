package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	// reviewRunPageSize is the most runs one page holds.
	reviewRunPageSize = 50
)

// knownReviewType is the closed set of review types this code renders. Any other type is answered as not found for
// everyone, admins included.
func knownReviewType(typ string) bool {
	switch typ {
	case "cost", "security", "reliability", "capacity":
		return true
	}
	return false
}

// reviewReader decides who may read a review run. It wraps the phase 2 reader, whose evidence rule it shares, and adds
// the tags that exist now. It is built for each request and never kept.
type reviewReader struct {
	investigationReader
	existingTags map[int64]bool
}

func (s *Server) newReviewReader(r *http.Request) (reviewReader, error) {
	base, err := s.newInvestigationReader(r)
	if err != nil {
		return reviewReader{}, err
	}
	tags, err := s.Store.ListTags(r.Context())
	if err != nil {
		return reviewReader{}, err
	}
	existing := make(map[int64]bool, len(tags))
	for _, tag := range tags {
		existing[int64(tag.ID)] = true
	}
	return reviewReader{investigationReader: base, existingTags: existing}, nil
}

// viewableTags are the tags that exist now and that the reader may View: the set a readable run must be covered by.
func (rd reviewReader) viewableTags() []int64 {
	out := make([]int64, 0, len(rd.existingTags))
	for tag := range rd.existingTags {
		if authz.Can(rd.subject, authz.View, authz.TagID(tag)) {
			out = append(out, tag)
		}
	}
	return out
}

// mayRead applies the read rule of a run: an unknown state or type is for nobody; an admin reads everything else; any
// other reader needs View on every tag the run covered, as the tags are now. A run that covered no tag, or a tag that
// is gone, is for admins only.
func (rd reviewReader) mayRead(v store.ReviewRun) bool {
	if !knownInvestigationState(v.State) || !knownReviewType(v.ReviewType) {
		return false
	}
	if rd.subject.Admin {
		return true
	}
	if len(v.CoveredTags) == 0 {
		return false
	}
	for _, tag := range v.CoveredTags {
		if !rd.existingTags[tag] || !authz.Can(rd.subject, authz.View, authz.TagID(tag)) {
			return false
		}
	}
	return true
}

// mayReadEvidence is the phase 2 rule of one item with one more: a review cites View evidence only, so an item stored
// with any other action is for nobody, admins included.
func (rd reviewReader) mayReadEvidence(e store.InvestigationEvidence) bool {
	return e.Action == "view" && rd.investigationReader.mayReadEvidence(e)
}

// reviewCursor is the position after a run: its requested_at in microseconds, as the database keeps it, and its id.
func reviewCursor(v store.ReviewRun) string {
	return fmt.Sprintf("%d.%d", v.RequestedAt.UnixMicro(), v.ID)
}

func parseReviewCursor(raw string) (time.Time, int64, bool) {
	micros, id, found := strings.Cut(raw, ".")
	if !found {
		return time.Time{}, 0, false
	}
	at, err := strconv.ParseInt(micros, 10, 64)
	runID, ok := canonicalID(id)
	if err != nil || at <= 0 || !ok || strconv.FormatInt(at, 10) != micros {
		return time.Time{}, 0, false
	}
	return time.UnixMicro(at).UTC(), runID, true
}

// listReviewRuns answers GET /api/reviews/runs: the newest runs the caller may read. The reader rule is part of the
// query, so a page holds readable runs only and a cursor is always the position of one of them.
func (s *Server) listReviewRuns(w http.ResponseWriter, r *http.Request) {
	limit := reviewRunPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.fail(w, invalid("limit must be a positive number."))
			return
		}
		limit = min(n, reviewRunPageSize)
	}
	filter := store.ReadableReviewRunFilter{Limit: limit + 1}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		at, id, ok := parseReviewCursor(raw)
		if !ok {
			s.fail(w, invalid("cursor is not valid."))
			return
		}
		filter.BeforeAt, filter.BeforeID = at, id
	}
	reader, err := s.newReviewReader(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	filter.Admin = reader.subject.Admin
	filter.ViewableTags = reader.viewableTags()
	runs, err := s.Store.ListReadableReviewRuns(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := ReviewRunsDTO{Items: make([]ReviewRunDTO, 0, min(limit, len(runs)))}
	var last store.ReviewRun
	for _, v := range runs {
		// The query applied the same rule; the check here is the final word on each row.
		if !reader.mayRead(v) {
			continue
		}
		if len(out.Items) == limit {
			out.NextCursor = reviewCursor(last)
			break
		}
		out.Items = append(out.Items, reviewRunDTO(v))
		last = v
	}
	writeJSON(w, http.StatusOK, out)
}

// getReviewRun answers GET /api/reviews/runs/{id}: not found unless this reader may read the run, else the summary and
// the evidence this reader may see, computed from the current permissions on every request.
func (s *Server) getReviewRun(w http.ResponseWriter, r *http.Request) {
	id, ok := canonicalID(r.PathValue("id"))
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	v, err := s.Store.GetReviewRun(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	reader, err := s.newReviewReader(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if !reader.mayRead(v) {
		s.fail(w, store.ErrNotFound)
		return
	}
	// Evidence is loaded only for a reader who may read the run at all.
	evidence, err := s.Store.ListReviewRunEvidence(r.Context(), v.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	keys := make([]model.ServiceKey, 0, len(evidence))
	for _, e := range evidence {
		keys = append(keys, evidenceKey(e))
	}
	if err := s.know(r, reader.investigationReader, keys); err != nil {
		s.fail(w, err)
		return
	}
	out := ReviewRunDetailDTO{ReviewRunDTO: reviewRunDTO(v), Evidence: make([]InvestigationEvidenceDTO, 0, len(evidence))}
	for _, e := range evidence {
		if !reader.mayReadEvidence(e) {
			out.EvidenceHidden++
			continue
		}
		out.Evidence = append(out.Evidence, InvestigationEvidenceDTO{
			Ordinal: e.Ordinal, Tool: e.Tool, Action: e.Action, ServiceID: evidenceKey(e).ID(), Service: e.ServiceName,
			Note: e.Note, Excerpt: e.Excerpt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
