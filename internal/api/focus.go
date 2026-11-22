package api

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	focusCappedMessage      = "Normal pace (live limit reached)"
	focusUnavailableMessage = "Live refresh unavailable"
	// minFocusEvidenceAge is the least age at which a fresh entry stops counting as evidence: it must be newer than two
	// effective intervals, and never less than this.
	minFocusEvidenceAge = 10 * time.Second
	// focusFirstReadWait is how long after an open a page without evidence is still waiting for the poller's first read
	// rather than being served by nobody.
	focusFirstReadWait = 12 * time.Second
	// Opens that carry a poll-now signal are limited per user: a burst of ten (clicking through a list of pages), then two
	// a second. The heartbeat itself is never refused.
	focusOpenBurst           = 10
	focusOpenRefillPerSecond = 2.0
)

// focusOpens remembers when each object was last opened through this task.
type focusOpens struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (o *focusOpens) mark(member string, now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.at == nil {
		o.at = map[string]time.Time{}
	}
	o.at[member] = now
	for m, t := range o.at { // forget old opens
		if now.Sub(t) > time.Minute {
			delete(o.at, m)
		}
	}
}

func (o *focusOpens) recent(member string, now time.Time) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.at[member]
	return ok && now.Sub(t) < focusFirstReadWait
}

// focusObject resolves the request to the object it names. A kind other than cluster or service is an error the
// caller turns into 422; an id that does not parse is ok=false (the page's own 404).
func focusObject(req FocusRequest) (obj focus.Object, ok bool, kindErr error) {
	switch focus.Kind(req.Kind) {
	case focus.KindCluster:
		key, err := model.ParseClusterID(req.ID)
		if err != nil {
			return focus.Object{}, false, nil
		}
		obj = focus.Object{Kind: focus.KindCluster, Cluster: key}
	case focus.KindService:
		key, err := model.ParseServiceID(req.ID)
		if err != nil {
			return focus.Object{}, false, nil
		}
		obj = focus.Object{Kind: focus.KindService, Cluster: key.ClusterKey, Service: key.Service}
	default:
		return focus.Object{}, false, errors.New("kind must be cluster or service")
	}
	return obj, obj.Valid(), nil
}

// postFocus is the heartbeat of a page that is open in a visible tab. The visibility check comes before anything is
// registered or published, so a user cannot make Harbormaster poll an object they may not see; a failure is the same 404
// the page itself answers.
func (s *Server) postFocus(w http.ResponseWriter, r *http.Request) {
	var req FocusRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.fail(w, err)
		return
	}
	obj, ok, kindErr := focusObject(req)
	if kindErr != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid", kindErr.Error())
		return
	}
	if !ok {
		s.countFocus("not_found")
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	if obj.Kind == focus.KindCluster {
		_, err = s.Store.GetCluster(ctx, obj.Cluster, sc.Vis)
	} else {
		_, err = s.Store.GetServiceView(ctx, obj.ServiceKey(), sc.Vis)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.countFocus("not_found")
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}

	if s.Focus == nil || (s.KVHealth != nil && !s.KVHealth.Up()) {
		s.focusUnavailable(w)
		return
	}
	limits := focus.Limits{TTL: s.Cfg.Focus.TTL, MaxObjects: s.Cfg.Focus.MaxObjects, MaxPerUser: s.Cfg.Focus.MaxPerUser}
	admission, err := s.Focus.Heartbeat(ctx, obj, sc.Subject.UserID, limits)
	if err != nil {
		s.Log.Warn("focus heartbeat failed", "err", err)
		s.focusUnavailable(w)
		return
	}
	s.countFocus(string(admission))
	if req.Open {
		s.focusOpens.mark(obj.Member(), s.Now())
		// Whatever the admission: a capped page still gets its immediate read. A lost signal is repaired by the
		// poller's own registry read (not for a capped object, which only gets this one), so an error is ignored. A user
		// past the open limit skips the signal; the registry read starts a registered object within a second.
		if s.Bus != nil && s.allowRate(ctx, rateFocusOpen, strconv.FormatInt(sc.Subject.UserID, 10), focusOpenBurst, focusOpenRefillPerSecond) {
			_ = s.Bus.Publish(ctx, signal.PollNow, obj.Member())
		}
	}

	switch admission {
	case focus.Capped:
		s.writeFocus(w, FocusDTO{State: "capped", IntervalSeconds: s.servicesIntervalSeconds(), Message: focusCappedMessage})
		return
	case focus.UserCapped:
		s.writeFocus(w, FocusDTO{
			State: "user_capped", IntervalSeconds: s.servicesIntervalSeconds(),
			Message: fmt.Sprintf("You have %d pages refreshing live. Close one to refresh this page live.", s.Cfg.Focus.MaxPerUser),
		})
		return
	}

	dto, err := s.focusEvidence(ctx, obj, req.Open || admission == focus.Admitted || s.focusOpens.recent(obj.Member(), s.Now()))
	if err != nil {
		s.Log.Warn("focus evidence read failed", "err", err)
		s.focusUnavailable(w)
		return
	}
	s.writeFocus(w, dto)
}

// focusEvidence answers from what the poller wrote: a fresh entry newer than two effective intervals (at least 10 s).
// Without one, a page that has just opened is live with no refreshed_at; any other is unavailable, because nothing proves
// the signal reached an owning poller.
func (s *Server) focusEvidence(ctx context.Context, obj focus.Object, justOpened bool) (FocusDTO, error) {
	fresh, ok, err := s.Focus.Fresh(ctx, obj)
	if err != nil {
		return FocusDTO{}, err
	}
	if ok && s.Now().Sub(fresh.At) <= max(2*fresh.Interval, minFocusEvidenceAge) {
		at := fresh.At
		return FocusDTO{State: fresh.State, IntervalSeconds: int(math.Ceil(fresh.Interval.Seconds())), RefreshedAt: &at}, nil
	}
	if justOpened {
		return FocusDTO{State: "live", IntervalSeconds: int(s.Cfg.Focus.Interval.Seconds())}, nil
	}
	s.countFocus("unavailable")
	return FocusDTO{State: "unavailable", IntervalSeconds: s.servicesIntervalSeconds(), Message: focusUnavailableMessage}, nil
}

// deleteFocus releases the user's claim on an object. It reveals nothing, so there is no visibility lookup, and it
// answers 204 whatever happens.
func (s *Server) deleteFocus(w http.ResponseWriter, r *http.Request) {
	defer w.WriteHeader(http.StatusNoContent)
	var req FocusRequest
	if err := decodeJSON(w, r, &req); err != nil || s.Focus == nil {
		return
	}
	obj, ok, kindErr := focusObject(req)
	if kindErr != nil || !ok {
		return
	}
	if sc, err := s.scopeFrom(r); err == nil {
		_ = s.Focus.Release(r.Context(), obj, sc.Subject.UserID)
	}
}

func (s *Server) focusUnavailable(w http.ResponseWriter) {
	s.countFocus("unavailable")
	s.writeFocus(w, FocusDTO{State: "unavailable", IntervalSeconds: s.servicesIntervalSeconds(), Message: focusUnavailableMessage, Failed: true})
}

func (s *Server) writeFocus(w http.ResponseWriter, dto FocusDTO) { writeJSON(w, http.StatusOK, dto) }

func (s *Server) servicesIntervalSeconds() int { return int(s.Cfg.Poll.Services.Seconds()) }

func (s *Server) countFocus(result string) { s.Telemetry.CountFocusAdmission(result) }
