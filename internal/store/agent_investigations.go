package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// Investigation states and reasons. Anything else is refused on the way in.
const (
	InvestigationRequested = "requested"
	InvestigationRunning   = "running"
	InvestigationDone      = "done"
	InvestigationFailed    = "failed"
	InvestigationSkipped   = "skipped"
)

var investigationReasons = map[string]bool{
	"budget": true, "not_visible": true, "not_configured": true, "rate_limited": true, "expired": true,
	"agent_unavailable": true, "limit_reached": true, "no_result": true, "interrupted": true, "error": true,
}

// Caps that the schema also enforces; checked here first so a refusal is an error, not a constraint failure.
const (
	maxInvestigationAttempts = 2
	maxInvestigationMembers  = 50
	maxInvestigationEvidence = 12
	maxEvidenceExcerptBytes  = 1500
	maxEvidenceNoteRunes     = 300
	maxInvestigationSummary  = 1200
)

// InvestigationMember is one service of a group investigation, as it was when the group was requested.
type InvestigationMember struct {
	Cluster string `json:"cluster"`
	Service string `json:"service"`
}

// Investigation is one automatic investigation of an alert, a cluster entry or an account entry.
type Investigation struct {
	ID                                          int64
	SubjectKind, SubjectKey                     string
	AccountID, Region, ClusterName, ServiceName string
	Members                                     []InvestigationMember
	AlertKind, AlertTitle, AlertText            string
	AlertOpenedAt                               time.Time
	State, Reason                               string
	Attempts                                    int
	// ClaimedUntil is the lease while running, and the earliest retry time while requested.
	ClaimedUntil          *time.Time
	RunID                 *uuid.UUID
	Provider, Model       string
	Summary               string
	SummaryReplaced       bool
	EvidenceDropped       int
	RequestedAt           time.Time
	StartedAt, FinishedAt *time.Time
}

// InvestigationEvidence is one cited unit. Tool and the service fields come from code, never from the model.
type InvestigationEvidence struct {
	Ordinal                                     int
	Tool, Action                                string // Action is "view" or "logs"
	AccountID, Region, ClusterName, ServiceName string
	Note, Excerpt                               string
}

// InvestigationResult is what FinishInvestigation stores.
type InvestigationResult struct {
	Summary         string
	SummaryReplaced bool
	Evidence        []InvestigationEvidence
	EvidenceDropped int
}

// InvestigationFilter pages the list newest first; BeforeAt and BeforeID are the keyset cursor.
type InvestigationFilter struct {
	State, SubjectKind string
	BeforeAt           *time.Time
	BeforeID           int64
	Limit              int
}

// InvestigationKey identifies the open alert an investigation is attached to.
type InvestigationKey struct {
	model.ServiceKey
	Kind string
}

const investigationColumnNames = `id, subject_kind, subject_key, account_id, region, cluster_name, service_name, members,
	alert_kind, alert_title, alert_text, alert_opened_at, state, reason, attempts, claimed_until, run_id, provider, model,
	summary, summary_replaced, evidence_dropped, requested_at, started_at, finished_at`

var investigationColumns = investigationColumnList("")

func investigationColumnList(prefix string) string {
	names := strings.Split(strings.ReplaceAll(investigationColumnNames, "\n\t", " "), ",")
	for i := range names {
		names[i] = prefix + strings.TrimSpace(names[i])
	}
	return strings.Join(names, ", ")
}

func scanInvestigation(row pgx.Row) (Investigation, error) {
	var v Investigation
	var members []byte
	err := row.Scan(&v.ID, &v.SubjectKind, &v.SubjectKey, &v.AccountID, &v.Region, &v.ClusterName, &v.ServiceName, &members,
		&v.AlertKind, &v.AlertTitle, &v.AlertText, &v.AlertOpenedAt, &v.State, &v.Reason, &v.Attempts, &v.ClaimedUntil, &v.RunID,
		&v.Provider, &v.Model, &v.Summary, &v.SummaryReplaced, &v.EvidenceDropped, &v.RequestedAt, &v.StartedAt, &v.FinishedAt)
	if err != nil {
		return Investigation{}, err
	}
	if err := json.Unmarshal(members, &v.Members); err != nil {
		return Investigation{}, fmt.Errorf("decode investigation members: %w", err)
	}
	return v, nil
}

func collectInvestigations(rows pgx.Rows) ([]Investigation, error) {
	defer rows.Close()
	out := []Investigation{}
	for rows.Next() {
		v, err := scanInvestigation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// investigationSubject derives the subject of an incident: the service of an alert, the cluster or account entry of
// a group. The route is not part of it, so two routes watching one entry share an investigation. ok is false for a
// kind this code does not know, which makes the incident no candidate.
func investigationSubject(scopeKind, account, region, cluster, service string) (kind, key string, ok bool) {
	switch scopeKind {
	case "alert":
		if service == "" {
			return "", "", false
		}
		return "service", fmt.Sprintf("service/%s/%s/%s/%s", account, region, cluster, service), true
	case "cluster":
		return "cluster", fmt.Sprintf("cluster/%s/%s/%s", account, region, cluster), true
	case "account":
		return "account", fmt.Sprintf("account/%s/%s", account, region), true
	}
	return "", "", false
}

// RecordInvestigationRequests is the only writer of investigation requests. In one transaction, serialised by an
// advisory lock, it takes unresolved incidents that were announced (an opened delivery, not cancelled, created
// inside the window) on a route that is enabled and has the switch on, and that are not linked yet, oldest first and
// at most limit. Each joins the investigation of its subject requested inside the window, or opens one. created counts
// the investigations opened; linked counts the incidents attached to an investigation that another incident opened.
func (s *Store) RecordInvestigationRequests(ctx context.Context, now time.Time, window time.Duration, limit int) (created, linked int, err error) {
	if limit <= 0 {
		return 0, 0, nil
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("record investigation requests: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('agent_investigations'))`); err != nil {
		return 0, 0, fmt.Errorf("record investigation requests: lock: %w", err)
	}
	windowStart := now.Add(-window)
	type candidate struct {
		id                                           int64
		scopeKind, account, region, cluster, service string
		kind, title, text                            string
		openedAt                                     time.Time
		members                                      []byte
	}
	rows, err := tx.Query(ctx, `select i.id, i.scope_kind, i.account_id, i.region, i.cluster_name, i.service_name,
			i.kind, i.title, i.text, i.opened_at, i.members
		from notification_incidents i
		where i.resolved_at is null
		  and i.scope_kind in ('alert', 'cluster', 'account')
		  and (i.scope_kind <> 'alert' or i.service_name <> '')
		  and not exists (select 1 from agent_investigation_incidents l where l.incident_id = i.id)
		  and exists (select 1 from notification_deliveries d
		    join notification_routes r on r.id = d.route_id
		    where d.incident_id = i.id and d.event = 'opened' and d.status <> 'cancelled' and d.created_at > $1
		      and r.enabled and r.investigate)
		order by i.opened_at, i.id
		limit $2`, windowStart, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("record investigation requests: candidates: %w", err)
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.scopeKind, &c.account, &c.region, &c.cluster, &c.service, &c.kind, &c.title, &c.text, &c.openedAt, &c.members); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("record investigation requests: scan: %w", err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("record investigation requests: candidates: %w", err)
	}

	for _, c := range candidates {
		kind, key, ok := investigationSubject(c.scopeKind, c.account, c.region, c.cluster, c.service)
		if !ok {
			continue
		}
		var investigationID int64
		err := tx.QueryRow(ctx, `select id from agent_investigations where subject_key = $1 and requested_at > $2
			order by requested_at desc, id desc limit 1`, key, windowStart).Scan(&investigationID)
		switch {
		case err == nil:
			linked++
		case errors.Is(err, pgx.ErrNoRows):
			members, merr := investigationMembers(kind, c.members)
			if merr != nil {
				return 0, 0, fmt.Errorf("record investigation requests: %w", merr)
			}
			// A group's cluster and service are not part of the group's own fields.
			cluster, service := c.cluster, c.service
			if err := tx.QueryRow(ctx, `insert into agent_investigations
				(subject_kind, subject_key, account_id, region, cluster_name, service_name, members,
				 alert_kind, alert_title, alert_text, alert_opened_at, requested_at)
				values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) returning id`,
				kind, key, c.account, c.region, cluster, service, members, c.kind, c.title, c.text, c.openedAt, now).Scan(&investigationID); err != nil {
				return 0, 0, fmt.Errorf("record investigation requests: insert: %w", err)
			}
			created++
		default:
			return 0, 0, fmt.Errorf("record investigation requests: lookup: %w", err)
		}
		if _, err := tx.Exec(ctx, `insert into agent_investigation_incidents (investigation_id, incident_id) values ($1, $2)`, investigationID, c.id); err != nil {
			return 0, 0, fmt.Errorf("record investigation requests: link: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("record investigation requests: %w", err)
	}
	return created, linked, nil
}

// investigationMembers snapshots a group's members as {cluster, service}, at most 50. An alert has none.
func investigationMembers(subjectKind string, raw []byte) ([]byte, error) {
	out := []InvestigationMember{}
	if subjectKind != "service" {
		var incidentMembers []IncidentMember
		if err := json.Unmarshal(raw, &incidentMembers); err != nil {
			return nil, fmt.Errorf("decode incident members: %w", err)
		}
		for _, m := range incidentMembers {
			if len(out) == maxInvestigationMembers {
				break
			}
			out = append(out, InvestigationMember{Cluster: m.Cluster, Service: m.Service})
		}
	}
	return json.Marshal(out)
}

// ClaimInvestigation leases the oldest due investigation to the caller: one that is requested with no retry time or
// a past one, or running with an expired lease and an attempt left. It returns ErrNotFound when nothing is due. A row
// that has no attempt left (a lease that expired on the last attempt, or a release that used it) is failed as
// interrupted here and not returned.
func (s *Store) ClaimInvestigation(ctx context.Context, now time.Time, lease time.Duration) (Investigation, error) {
	if _, err := s.Pool.Exec(ctx, `update agent_investigations set state = 'failed', reason = 'interrupted', finished_at = $1, claimed_until = null
		where id in (select id from agent_investigations
		  where state in ('requested', 'running') and attempts >= $2 and coalesce(claimed_until, '-infinity') <= $1
		  for update skip locked)`, now, maxInvestigationAttempts); err != nil {
		return Investigation{}, fmt.Errorf("claim investigation: fail interrupted: %w", err)
	}
	v, err := scanInvestigation(s.Pool.QueryRow(ctx, `update agent_investigations set state = 'running', attempts = attempts + 1, claimed_until = $2
		where id = (select id from agent_investigations
		  where attempts < $3 and (
		    (state = 'requested' and (claimed_until is null or claimed_until <= $1))
		    or (state = 'running' and claimed_until <= $1))
		  order by requested_at, id limit 1 for update skip locked)
		returning `+investigationColumns, now, now.Add(lease), maxInvestigationAttempts))
	if errors.Is(err, pgx.ErrNoRows) {
		return Investigation{}, ErrNotFound
	}
	if err != nil {
		return Investigation{}, fmt.Errorf("claim investigation: %w", err)
	}
	return v, nil
}

// StartInvestigationRun records the agent run of the claimed attempt. ErrConflict when the row is not running at
// that attempt.
func (s *Store) StartInvestigationRun(ctx context.Context, id int64, attempt int, runID uuid.UUID, provider, model string) error {
	tag, err := s.Pool.Exec(ctx, `update agent_investigations set run_id = $3, provider = $4, model = $5, started_at = now()
		where id = $1 and state = 'running' and attempts = $2`, id, attempt, runID, provider, model)
	if err != nil {
		return fmt.Errorf("start investigation run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

func validateInvestigationResult(state string, r *InvestigationResult) error {
	if r == nil {
		if state == InvestigationDone {
			return errors.New("finish investigation: a done investigation needs a result")
		}
		return nil
	}
	if state == InvestigationDone && strings.TrimSpace(r.Summary) == "" {
		return errors.New("finish investigation: a done investigation needs a summary")
	}
	if len([]rune(r.Summary)) > maxInvestigationSummary {
		return errors.New("finish investigation: summary is too long")
	}
	if len(r.Evidence) > maxInvestigationEvidence {
		return fmt.Errorf("finish investigation: %d evidence items, at most %d", len(r.Evidence), maxInvestigationEvidence)
	}
	if r.EvidenceDropped < 0 {
		return errors.New("finish investigation: negative dropped count")
	}
	for i, e := range r.Evidence {
		switch e.Action {
		case "view", "logs":
		default:
			return fmt.Errorf("finish investigation: evidence %d has unknown action %q", i+1, e.Action)
		}
		if e.Tool == "" || e.AccountID == "" || e.Region == "" || e.ClusterName == "" || e.ServiceName == "" {
			return fmt.Errorf("finish investigation: evidence %d has no tool or service", i+1)
		}
		if len(e.Excerpt) > maxEvidenceExcerptBytes {
			return fmt.Errorf("finish investigation: evidence %d excerpt is %d bytes, at most %d", i+1, len(e.Excerpt), maxEvidenceExcerptBytes)
		}
		if len([]rune(e.Note)) > maxEvidenceNoteRunes {
			return fmt.Errorf("finish investigation: evidence %d note is too long", i+1)
		}
		if strings.ContainsRune(e.Excerpt, 0) || strings.ContainsRune(e.Note, 0) {
			return fmt.Errorf("finish investigation: evidence %d has a NUL byte", i+1)
		}
	}
	return nil
}

// FinishInvestigation settles the claimed attempt as done or failed, writes the result and replaces the evidence in
// one transaction, and clears the lease. ErrConflict when the row is not running at that attempt (a newer holder
// owns it), and then nothing is written. A refused result is an error and nothing is written either.
func (s *Store) FinishInvestigation(ctx context.Context, id int64, attempt int, state, reason string, result *InvestigationResult, now time.Time) error {
	switch state {
	case InvestigationDone:
	case InvestigationFailed:
		if !investigationReasons[reason] {
			return fmt.Errorf("finish investigation: unknown reason %q", reason)
		}
	default:
		return fmt.Errorf("finish investigation: cannot finish as %q", state)
	}
	if err := validateInvestigationResult(state, result); err != nil {
		return err
	}
	if result == nil {
		result = &InvestigationResult{}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("finish investigation: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	tag, err := tx.Exec(ctx, `update agent_investigations set state = $3, reason = $4, summary = $5, summary_replaced = $6,
		evidence_dropped = $7, finished_at = $8, claimed_until = null
		where id = $1 and state = 'running' and attempts = $2`,
		id, attempt, state, reason, result.Summary, result.SummaryReplaced, result.EvidenceDropped, now)
	if err != nil {
		return fmt.Errorf("finish investigation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `delete from agent_investigation_evidence where investigation_id = $1`, id); err != nil {
		return fmt.Errorf("finish investigation: clear evidence: %w", err)
	}
	for i, e := range result.Evidence {
		if _, err := tx.Exec(ctx, `insert into agent_investigation_evidence
			(investigation_id, ordinal, tool, action, account_id, region, cluster_name, service_name, note, excerpt)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, i+1, e.Tool, e.Action, e.AccountID, e.Region, e.ClusterName, e.ServiceName, e.Note, e.Excerpt); err != nil {
			return fmt.Errorf("finish investigation: store evidence: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish investigation: %w", err)
	}
	return nil
}

// ReleaseInvestigation hands a claimed attempt back to the queue, to be claimed again from retryAt (zero: at once).
// countAttempt false gives the attempt back, for a run that never started. ErrConflict when the row is not running
// at that attempt.
func (s *Store) ReleaseInvestigation(ctx context.Context, id int64, attempt int, retryAt time.Time, countAttempt bool) error {
	var retry *time.Time
	if !retryAt.IsZero() {
		retry = &retryAt
	}
	tag, err := s.Pool.Exec(ctx, `update agent_investigations set state = 'requested', claimed_until = $3,
		attempts = case when $4 then attempts else attempts - 1 end
		where id = $1 and state = 'running' and attempts = $2`, id, attempt, retry, countAttempt)
	if err != nil {
		return fmt.Errorf("release investigation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// SkipInvestigation ends a requested or running investigation without a result. ErrNotFound for a missing row,
// ErrConflict for one that is already settled.
func (s *Store) SkipInvestigation(ctx context.Context, id int64, reason string, now time.Time) error {
	if !investigationReasons[reason] {
		return fmt.Errorf("skip investigation: unknown reason %q", reason)
	}
	tag, err := s.Pool.Exec(ctx, `update agent_investigations set state = 'skipped', reason = $2, finished_at = $3, claimed_until = null
		where id = $1 and state in ('requested', 'running')`, id, reason, now)
	if err != nil {
		return fmt.Errorf("skip investigation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.GetInvestigation(ctx, id); err != nil {
			return err
		}
		return ErrConflict
	}
	return nil
}

// InvestigationsStartedSince counts investigations whose latest run started at or after since; the hourly cap reads it.
func (s *Store) InvestigationsStartedSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	if err := s.Pool.QueryRow(ctx, `select count(*) from agent_investigations where started_at >= $1`, since).Scan(&n); err != nil {
		return 0, fmt.Errorf("count started investigations: %w", err)
	}
	return n, nil
}

// GetInvestigation returns one investigation, or ErrNotFound.
func (s *Store) GetInvestigation(ctx context.Context, id int64) (Investigation, error) {
	v, err := scanInvestigation(s.Pool.QueryRow(ctx, `select `+investigationColumns+` from agent_investigations where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Investigation{}, ErrNotFound
	}
	if err != nil {
		return Investigation{}, fmt.Errorf("get investigation: %w", err)
	}
	return v, nil
}

// ListInvestigationEvidence returns the stored evidence of an investigation in order.
func (s *Store) ListInvestigationEvidence(ctx context.Context, id int64) ([]InvestigationEvidence, error) {
	rows, err := s.Pool.Query(ctx, `select ordinal, tool, action, account_id, region, cluster_name, service_name, note, excerpt
		from agent_investigation_evidence where investigation_id = $1 order by ordinal`, id)
	if err != nil {
		return nil, fmt.Errorf("list investigation evidence: %w", err)
	}
	defer rows.Close()
	out := []InvestigationEvidence{}
	for rows.Next() {
		var e InvestigationEvidence
		if err := rows.Scan(&e.Ordinal, &e.Tool, &e.Action, &e.AccountID, &e.Region, &e.ClusterName, &e.ServiceName, &e.Note, &e.Excerpt); err != nil {
			return nil, fmt.Errorf("list investigation evidence: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListServiceInvestigations returns the investigations of one service, newest first.
func (s *Store) ListServiceInvestigations(ctx context.Context, key model.ServiceKey, limit int) ([]Investigation, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, `select `+investigationColumns+` from agent_investigations
		where subject_kind = 'service' and account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4
		order by requested_at desc, id desc limit $5`, key.AccountID, key.Region, key.Cluster, key.Service, limit)
	if err != nil {
		return nil, fmt.Errorf("list service investigations: %w", err)
	}
	out, err := collectInvestigations(rows)
	if err != nil {
		return nil, fmt.Errorf("list service investigations: %w", err)
	}
	return out, nil
}

// ListInvestigations returns investigations newest first, after the cursor when one is given.
func (s *Store) ListInvestigations(ctx context.Context, f InvestigationFilter) ([]Investigation, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `select `+investigationColumns+` from agent_investigations
		where ($1 = '' or state = $1) and ($2 = '' or subject_kind = $2)
		  and ($3::timestamptz is null or (requested_at, id) < ($3, $4))
		order by requested_at desc, id desc limit $5`, f.State, f.SubjectKind, f.BeforeAt, f.BeforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list investigations: %w", err)
	}
	out, err := collectInvestigations(rows)
	if err != nil {
		return nil, fmt.Errorf("list investigations: %w", err)
	}
	return out, nil
}

// InvestigationsForOpenIncidents returns, for each given service and alert kind with an open alert incident, the
// latest investigation linked to that incident.
func (s *Store) InvestigationsForOpenIncidents(ctx context.Context, keys []model.ServiceKey) (map[InvestigationKey]Investigation, error) {
	out := map[InvestigationKey]Investigation{}
	if len(keys) == 0 {
		return out, nil
	}
	accounts, regions, clusters, services := make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		accounts[i], regions[i], clusters[i], services[i] = k.AccountID, k.Region, k.Cluster, k.Service
	}
	rows, err := s.Pool.Query(ctx, `select distinct on (i.account_id, i.region, i.cluster_name, i.service_name, i.kind)
			i.kind, `+investigationColumnList("v.")+`
		from unnest($1::text[], $2::text[], $3::text[], $4::text[]) k(account_id, region, cluster_name, service_name)
		join notification_incidents i on i.account_id = k.account_id and i.region = k.region
		  and i.cluster_name = k.cluster_name and i.service_name = k.service_name
		  and i.scope_kind = 'alert' and i.resolved_at is null
		join agent_investigation_incidents l on l.incident_id = i.id
		join agent_investigations v on v.id = l.investigation_id
		order by i.account_id, i.region, i.cluster_name, i.service_name, i.kind, v.requested_at desc, v.id desc`,
		accounts, regions, clusters, services)
	if err != nil {
		return nil, fmt.Errorf("investigations for open incidents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var v Investigation
		var members []byte
		if err := rows.Scan(&kind, &v.ID, &v.SubjectKind, &v.SubjectKey, &v.AccountID, &v.Region, &v.ClusterName, &v.ServiceName, &members,
			&v.AlertKind, &v.AlertTitle, &v.AlertText, &v.AlertOpenedAt, &v.State, &v.Reason, &v.Attempts, &v.ClaimedUntil, &v.RunID,
			&v.Provider, &v.Model, &v.Summary, &v.SummaryReplaced, &v.EvidenceDropped, &v.RequestedAt, &v.StartedAt, &v.FinishedAt); err != nil {
			return nil, fmt.Errorf("investigations for open incidents: %w", err)
		}
		if err := json.Unmarshal(members, &v.Members); err != nil {
			return nil, fmt.Errorf("decode investigation members: %w", err)
		}
		key := InvestigationKey{ServiceKey: model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: v.ClusterName}, Service: v.ServiceName}, Kind: kind}
		out[key] = v
	}
	return out, rows.Err()
}

// DeleteInvestigationsBefore removes investigations settled (or, if never settled, requested) before t. Their
// evidence and incident links cascade.
func (s *Store) DeleteInvestigationsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from agent_investigations where coalesce(finished_at, requested_at) < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete investigations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAgentRunsBefore removes finished runs that started before both cutoff and monthStart. A run that is running,
// or finished but not yet audited (the reconciler still reads it), and every run of the current month stay, so the
// month's spend never shrinks. A zero monthStart deletes nothing.
func (s *Store) DeleteAgentRunsBefore(ctx context.Context, cutoff, monthStart time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from agent_runs
		where state <> 'running' and audited_at is not null and started_at < $1 and started_at < $2`, cutoff, monthStart)
	if err != nil {
		return 0, fmt.Errorf("delete agent runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
