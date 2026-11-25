package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	defaultInvestigationPage    = 50
	invalidInvestigationsCursor = "Invalid cursor."
)

// agentIdentityRoutes registers the admin routes of the agent identity and the list of past investigations; serve
// registers them only while HM_AGENT_URL is set, next to agentRoutes. Like those, they are session-only.
func (s *Server) agentIdentityRoutes(mux *http.ServeMux) {
	s.handle(mux, "PUT /api/agent/identity", s.putAgentIdentity, routeOptions{Admin: true})
	s.handle(mux, "GET /api/agent/investigations", s.listAgentInvestigations, routeOptions{Admin: true})
}

// AgentIdentityPermissionDTO is the access of the agent identity to one tag: View, and Logs when Logs is true.
type AgentIdentityPermissionDTO struct {
	TagID   int64  `json:"tag_id"`
	TagName string `json:"tag_name"`
	Logs    bool   `json:"logs"`
}

// AgentIdentityDTO is the agent identity and what it may read.
type AgentIdentityDTO struct {
	Username    string                       `json:"username"`
	Permissions []AgentIdentityPermissionDTO `json:"permissions"`
}

// agentIdentityDTO creates the identity when it does not exist yet, so the block is there from the first read.
// Permissions come from the subject the investigator would run as, so they show exactly what it can do.
func (s *Server) agentIdentityDTO(ctx context.Context) (AgentIdentityDTO, error) {
	identity, err := s.Store.EnsureAgentIdentity(ctx)
	if err != nil {
		return AgentIdentityDTO{}, err
	}
	subject, err := s.Store.LoadSubject(ctx, identity.ID)
	if err != nil {
		return AgentIdentityDTO{}, err
	}
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return AgentIdentityDTO{}, err
	}
	names := make(map[authz.TagID]string, len(tags))
	for _, tag := range tags {
		names[tag.ID] = tag.Name
	}
	out := AgentIdentityDTO{Username: identity.Username, Permissions: []AgentIdentityPermissionDTO{}}
	for tag, actions := range subject.Perms {
		if actions.Has(authz.View) {
			out.Permissions = append(out.Permissions, AgentIdentityPermissionDTO{TagID: int64(tag), TagName: names[tag], Logs: actions.Has(authz.Logs)})
		}
	}
	sort.Slice(out.Permissions, func(i, j int) bool { return out.Permissions[i].TagID < out.Permissions[j].TagID })
	return out, nil
}

// agentIdentityUpdate is the PUT body. Each entry is View on the tag, or View and Logs: nothing else can be said, and
// an unknown member is refused by the strict decoder.
type agentIdentityUpdate struct {
	Permissions []struct {
		TagID int64 `json:"tag_id"`
		Logs  bool  `json:"logs"`
	} `json:"permissions"`
}

var errUnknownIdentityTag = invalid("One of the tags no longer exists. Reload and try again.")

// putAgentIdentity replaces the identity's access. The tags are checked first, so an unknown tag is told apart from a
// missing identity; the store writes the access and its audit row (agent_identity_update) in one transaction.
func (s *Server) putAgentIdentity(w http.ResponseWriter, r *http.Request) {
	var body agentIdentityUpdate
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Permissions == nil {
		s.fail(w, invalid("Send the list of permissions."))
		return
	}
	perms := make(map[authz.TagID]authz.ActionSet, len(body.Permissions))
	for _, entry := range body.Permissions {
		tag := authz.TagID(entry.TagID)
		if entry.TagID < 1 {
			s.fail(w, invalid("Choose a tag."))
			return
		}
		if _, dup := perms[tag]; dup {
			s.fail(w, invalid("A tag can be listed once."))
			return
		}
		perms[tag] = authz.ActionSet(authz.View)
		if entry.Logs {
			perms[tag] |= authz.ActionSet(authz.Logs)
		}
	}
	ctx := r.Context()
	if err := s.allTagsExist(ctx, perms); err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.Store.EnsureAgentIdentity(ctx); err != nil {
		s.fail(w, err)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	if err := s.Store.SetAgentIdentityPermissions(ctx, perms, actor.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// A tag deleted since the check above is still an unknown tag, not a missing identity.
			if tagErr := s.allTagsExist(ctx, perms); tagErr != nil {
				err = tagErr
			}
		}
		s.fail(w, err)
		return
	}
	out, err := s.agentIdentityDTO(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) allTagsExist(ctx context.Context, perms map[authz.TagID]authz.ActionSet) error {
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return err
	}
	known := make(map[authz.TagID]bool, len(tags))
	for _, tag := range tags {
		known[tag.ID] = true
	}
	for tag := range perms {
		if !known[tag] {
			return errUnknownIdentityTag
		}
	}
	return nil
}

// AgentInvestigationDTO is one row of the list of past investigations. It carries no evidence: an admin opens one
// through the detail route.
type AgentInvestigationDTO struct {
	ID      int64  `json:"id"`
	State   string `json:"state"`
	Reason  string `json:"reason"`
	Subject struct {
		Kind      string `json:"kind"`
		ServiceID string `json:"service_id"`
		Service   string `json:"service"`
		Cluster   string `json:"cluster"`
		ClusterID string `json:"cluster_id"`
		AccountID string `json:"account_id"`
		Region    string `json:"region"`
	} `json:"subject"`
	Alert struct {
		Kind  string `json:"kind"`
		Title string `json:"title"`
	} `json:"alert"`
	RequestedAt     time.Time  `json:"requested_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
	Summary         string     `json:"summary"`
	SummaryReplaced bool       `json:"summary_replaced"`
	Provider        string     `json:"provider"`
	Model           string     `json:"model"`
	Attempts        int        `json:"attempts"`
	// CostUSD is what the latest run cost, in dollars; empty when there is no run row (skipped before any run, or the run
	// was deleted by retention). After a retry it is the last attempt only.
	CostUSD string `json:"cost_usd"`
}

// AgentInvestigationsDTO is the body of GET /api/agent/investigations. NextCursor is empty on the last page.
type AgentInvestigationsDTO struct {
	Items      []AgentInvestigationDTO `json:"items"`
	NextCursor string                  `json:"next_cursor"`
}

func encodeInvestigationCursor(at time.Time, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixNano(), 10) + ":" + strconv.FormatInt(id, 10)))
}

func decodeInvestigationCursor(token string) (time.Time, int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, 0, invalid(invalidInvestigationsCursor)
	}
	nanos, id, ok := strings.Cut(string(raw), ":")
	n, errN := strconv.ParseInt(nanos, 10, 64)
	i, errI := strconv.ParseInt(id, 10, 64)
	if !ok || errN != nil || errI != nil {
		return time.Time{}, 0, invalid(invalidInvestigationsCursor)
	}
	return time.Unix(0, n).UTC(), i, nil
}

// maxInvestigationPage is the configured page maximum, or 200 when none is set.
func maxInvestigationPage(configured int) int {
	if configured > 0 {
		return configured
	}
	return 200
}

// listAgentInvestigations answers GET /api/agent/investigations?cursor=&limit=, newest first.
func (s *Server) listAgentInvestigations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.InvestigationFilter{}
	if token := query.Get("cursor"); token != "" {
		at, id, err := decodeInvestigationCursor(token)
		if err != nil {
			s.fail(w, err)
			return
		}
		filter.BeforeAt, filter.BeforeID = &at, id
	}
	limit := defaultInvestigationPage
	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.fail(w, invalid("The limit must be a positive number."))
			return
		}
		limit = n
	}
	limit = min(limit, maxInvestigationPage(s.Cfg.PageSizeMax))
	filter.Limit = limit + 1 // one more than the page says whether there is a next one
	rows, err := s.Store.ListInvestigations(r.Context(), filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeInvestigationCursor(rows[limit-1].RequestedAt, rows[limit-1].ID)
	}
	runs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		if row.RunID != nil {
			runs = append(runs, *row.RunID)
		}
	}
	costs, err := s.Store.AgentRunCosts(r.Context(), runs)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := AgentInvestigationsDTO{Items: make([]AgentInvestigationDTO, 0, len(rows)), NextCursor: next}
	for _, row := range rows {
		cost := ""
		if row.RunID != nil {
			if micro, ok := costs[*row.RunID]; ok {
				cost = formatUSD(micro, true)
			}
		}
		out.Items = append(out.Items, investigationRowDTO(row, cost))
	}
	writeJSON(w, http.StatusOK, out)
}

func investigationRowDTO(v store.Investigation, costUSD string) AgentInvestigationDTO {
	dto := AgentInvestigationDTO{
		ID: v.ID, State: v.State, Reason: v.Reason, RequestedAt: v.RequestedAt, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt,
		Summary: v.Summary, SummaryReplaced: v.SummaryReplaced, Provider: v.Provider, Model: v.Model, Attempts: v.Attempts,
		CostUSD: costUSD,
	}
	dto.Subject.Kind, dto.Subject.AccountID, dto.Subject.Region = v.SubjectKind, v.AccountID, v.Region
	dto.Subject.Cluster, dto.Subject.Service = v.ClusterName, v.ServiceName
	cluster := model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: v.ClusterName}
	switch v.SubjectKind {
	case "service":
		dto.Subject.ClusterID = cluster.ID()
		dto.Subject.ServiceID = model.ServiceKey{ClusterKey: cluster, Service: v.ServiceName}.ID()
	case "cluster":
		dto.Subject.ClusterID = cluster.ID()
	}
	dto.Alert.Kind, dto.Alert.Title = v.AlertKind, v.AlertTitle
	return dto
}
