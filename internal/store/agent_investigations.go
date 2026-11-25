package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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
// MaxInvestigationMembers is exported because a group stored at the cap may have been cut, which the API treats as
// "members unknown": only admins read it.
const (
	maxInvestigationAttempts = 2
	MaxInvestigationMembers  = 50
	maxInvestigationEvidence = 12
	maxEvidenceExcerptBytes  = 1500
	maxEvidenceNoteRunes     = 300
	maxInvestigationSummary  = 1200
)

// minInvestigationRetention is the youngest a settled investigation can be and still be deleted, whatever the
// configured retention (the config refuses a shorter non-zero value; this is the same floor for a direct caller).
const minInvestigationRetention = 48 * time.Hour

// ErrInvestigationText is returned, before any SQL runs, for a text field that is not valid UTF-8, holds a NUL
// byte, or is longer than its cap.
var ErrInvestigationText = errors.New("investigation text refused")

// Claim is the fence of one claim. ClaimInvestigation returns it; every later write by the holder (start, renew,
// release, finish, skip) must carry it. A row whose attempt or claim_seq differs, or that is not running, gives
// ErrConflict and nothing is written, so a holder that was paused past its lease cannot touch the row a newer holder
// owns, even when a free release reused the same attempt number.
type Claim struct {
	InvestigationID int64
	Attempt         int
	Seq             int64
}

// Claim returns the fence of the claim that produced v.
func (v Investigation) Claim() Claim {
	return Claim{InvestigationID: v.ID, Attempt: v.Attempts, Seq: v.ClaimSeq}
}

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
	ClaimSeq                                    int64
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
	alert_kind, alert_title, alert_text, alert_opened_at, state, reason, attempts, claim_seq, claimed_until, run_id, provider, model,
	summary, summary_replaced, evidence_dropped, requested_at, started_at, finished_at`

var investigationColumns = investigationColumnList("")

func investigationColumnList(prefix string) string {
	names := strings.Split(strings.ReplaceAll(investigationColumnNames, "\n\t", " "), ",")
	for i := range names {
		names[i] = prefix + strings.TrimSpace(names[i])
	}
	return strings.Join(names, ", ")
}

func scanInvestigation(row pgx.Row) (Investigation, error) { return scanInvestigationWith(row) }

// scanInvestigationWith scans the investigation columns after any leading destinations.
func scanInvestigationWith(row pgx.Row, lead ...any) (Investigation, error) {
	var v Investigation
	var members []byte
	dest := append(lead, &v.ID, &v.SubjectKind, &v.SubjectKey, &v.AccountID, &v.Region, &v.ClusterName, &v.ServiceName, &members,
		&v.AlertKind, &v.AlertTitle, &v.AlertText, &v.AlertOpenedAt, &v.State, &v.Reason, &v.Attempts, &v.ClaimSeq, &v.ClaimedUntil, &v.RunID,
		&v.Provider, &v.Model, &v.Summary, &v.SummaryReplaced, &v.EvidenceDropped, &v.RequestedAt, &v.StartedAt, &v.FinishedAt)
	if err := row.Scan(dest...); err != nil {
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
			if len(out) == MaxInvestigationMembers {
				break
			}
			out = append(out, InvestigationMember{Cluster: m.Cluster, Service: m.Service})
		}
	}
	return json.Marshal(out)
}

// checkInvestigationText is the one text rule for every field a holder supplies: valid UTF-8, no NUL, at most
// maxRunes characters and maxBytes bytes (0 = no byte cap).
func checkInvestigationText(field, text string, maxRunes, maxBytes int) error {
	switch {
	case !utf8.ValidString(text):
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvestigationText, field)
	case strings.ContainsRune(text, 0):
		return fmt.Errorf("%w: %s has a NUL byte", ErrInvestigationText, field)
	case utf8.RuneCountInString(text) > maxRunes:
		return fmt.Errorf("%w: %s is longer than %d characters", ErrInvestigationText, field, maxRunes)
	case maxBytes > 0 && len(text) > maxBytes:
		return fmt.Errorf("%w: %s is longer than %d bytes", ErrInvestigationText, field, maxBytes)
	}
	return nil
}

// fencedInvestigation is the predicate every holder write carries: $1 id, $2 attempt, $3 claim_seq.
const fencedInvestigation = `id = $1 and state = 'running' and attempts = $2 and claim_seq = $3`

// ClaimInvestigation leases the oldest due investigation to the caller: one that is requested with no retry time or
// a past one, or running with an expired lease. It returns ErrNotFound when nothing is due. One statement either
// claims the row or, when it has no attempt left (a lease that expired on the last attempt, or a release that used
// it), fails it as interrupted; a failed row is not returned and the next due row is tried.
func (s *Store) ClaimInvestigation(ctx context.Context, now time.Time, lease time.Duration) (Investigation, Claim, error) {
	for {
		v, err := scanInvestigation(s.Pool.QueryRow(ctx, `update agent_investigations set
			state = case when attempts < $3 then 'running' else 'failed' end,
			reason = case when attempts < $3 then reason else 'interrupted' end,
			finished_at = case when attempts < $3 then finished_at else $1::timestamptz end,
			attempts = case when attempts < $3 then attempts + 1 else attempts end,
			claim_seq = case when attempts < $3 then claim_seq + 1 else claim_seq end,
			claimed_until = case when attempts < $3 then $2::timestamptz else null end
			where id = (select id from agent_investigations
			  where (state = 'requested' and (claimed_until is null or claimed_until <= $1))
			     or (state = 'running' and claimed_until <= $1)
			  order by requested_at, id limit 1 for update skip locked)
			returning `+investigationColumns, now, now.Add(lease), maxInvestigationAttempts))
		if errors.Is(err, pgx.ErrNoRows) {
			return Investigation{}, Claim{}, ErrNotFound
		}
		if err != nil {
			return Investigation{}, Claim{}, fmt.Errorf("claim investigation: %w", err)
		}
		if v.State == InvestigationFailed {
			continue
		}
		return v, v.Claim(), nil
	}
}

// fencedUpdate runs an update whose where clause is fencedInvestigation and maps no rows to ErrConflict.
func (s *Store) fencedUpdate(ctx context.Context, what string, c Claim, setSQL string, args ...any) error {
	all := append([]any{c.InvestigationID, c.Attempt, c.Seq}, args...)
	tag, err := s.Pool.Exec(ctx, `update agent_investigations set `+setSQL+` where `+fencedInvestigation, all...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	return nil
}

// StartInvestigationRun records the agent run of the claimed attempt at now. ErrConflict when the claim is stale.
func (s *Store) StartInvestigationRun(ctx context.Context, c Claim, runID uuid.UUID, provider, model string, now time.Time) error {
	if err := checkInvestigationText("provider", provider, 200, 0); err != nil {
		return err
	}
	if err := checkInvestigationText("model", model, 200, 0); err != nil {
		return err
	}
	return s.fencedUpdate(ctx, "start investigation run", c, `run_id = $4, provider = $5, model = $6, started_at = $7`, runID, provider, model, now)
}

// RenewInvestigation extends the lease of a claim to now + lease. ErrConflict when the claim is stale.
func (s *Store) RenewInvestigation(ctx context.Context, c Claim, now time.Time, lease time.Duration) error {
	return s.fencedUpdate(ctx, "renew investigation", c, `claimed_until = $4`, now.Add(lease))
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
	if err := checkInvestigationText("summary", r.Summary, maxInvestigationSummary, 0); err != nil {
		return err
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
		for field, text := range map[string]string{"tool": e.Tool, "account": e.AccountID, "region": e.Region, "cluster": e.ClusterName, "service": e.ServiceName} {
			if err := checkInvestigationText(fmt.Sprintf("evidence %d %s", i+1, field), text, 255, 0); err != nil {
				return err
			}
		}
		if err := checkInvestigationText(fmt.Sprintf("evidence %d note", i+1), e.Note, maxEvidenceNoteRunes, 0); err != nil {
			return err
		}
		if err := checkInvestigationText(fmt.Sprintf("evidence %d excerpt", i+1), e.Excerpt, maxEvidenceExcerptBytes, maxEvidenceExcerptBytes); err != nil {
			return err
		}
	}
	return nil
}

// FinishInvestigation settles the claimed attempt as done (no reason) or failed (a fixed reason), writes the result
// and replaces the evidence in one transaction, and clears the lease. ErrConflict when the claim is stale, and then
// nothing is written. A refused result is an error and nothing is written either.
func (s *Store) FinishInvestigation(ctx context.Context, c Claim, state, reason string, result *InvestigationResult, now time.Time) error {
	switch state {
	case InvestigationDone:
		if reason != "" {
			return fmt.Errorf("finish investigation: a done investigation has no reason, got %q", reason)
		}
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
	tag, err := tx.Exec(ctx, `update agent_investigations set state = $4, reason = $5, summary = $6, summary_replaced = $7,
		evidence_dropped = $8, finished_at = $9, claimed_until = null
		where `+fencedInvestigation,
		c.InvestigationID, c.Attempt, c.Seq, state, reason, result.Summary, result.SummaryReplaced, result.EvidenceDropped, now)
	if err != nil {
		return fmt.Errorf("finish investigation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrConflict
	}
	if _, err := tx.Exec(ctx, `delete from agent_investigation_evidence where investigation_id = $1`, c.InvestigationID); err != nil {
		return fmt.Errorf("finish investigation: clear evidence: %w", err)
	}
	for i, e := range result.Evidence {
		if _, err := tx.Exec(ctx, `insert into agent_investigation_evidence
			(investigation_id, ordinal, tool, action, account_id, region, cluster_name, service_name, note, excerpt)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			c.InvestigationID, i+1, e.Tool, e.Action, e.AccountID, e.Region, e.ClusterName, e.ServiceName, e.Note, e.Excerpt); err != nil {
			return fmt.Errorf("finish investigation: store evidence: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("finish investigation: %w", err)
	}
	return nil
}

// ReleaseInvestigation hands a claimed attempt back to the queue, to be claimed again from retryAt (zero: at once).
// countAttempt false gives the attempt back, for a run that never started; when that returns the row to no attempts
// started_at is cleared too (run_id stays, so the orphaned run can still be ended), so the hourly count does not
// include it. ErrConflict when the claim is stale.
func (s *Store) ReleaseInvestigation(ctx context.Context, c Claim, retryAt time.Time, countAttempt bool) error {
	var retry *time.Time
	if !retryAt.IsZero() {
		retry = &retryAt
	}
	return s.fencedUpdate(ctx, "release investigation", c, `state = 'requested', claimed_until = $4,
		started_at = case when $5 or attempts > 1 then started_at else null end,
		attempts = case when $5 then attempts else attempts - 1 end`, retry, countAttempt)
}

// SkipInvestigation ends a running investigation without a result, for a claim the caller holds. Nothing in the
// investigator skips a row it has not claimed (it claims, then decides), so a never-claimed requested row has no
// skip path. ErrConflict when the claim is stale.
func (s *Store) SkipInvestigation(ctx context.Context, c Claim, reason string, now time.Time) error {
	if !investigationReasons[reason] {
		return fmt.Errorf("skip investigation: unknown reason %q", reason)
	}
	return s.fencedUpdate(ctx, "skip investigation", c, `state = 'skipped', reason = $4, finished_at = $5, claimed_until = null`, reason, now)
}

// InvestigationsStartedSince counts investigations whose latest run started at or after since; the hourly cap reads
// it. The count and the start that follows are separate statements, so holders that check at the same moment can
// each start one run: the cap can overshoot by up to the number of concurrent holders. A free release clears
// started_at, so a run that never began is not counted.
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
		v, err := scanInvestigationWith(rows, &kind)
		if err != nil {
			return nil, fmt.Errorf("investigations for open incidents: %w", err)
		}
		key := InvestigationKey{ServiceKey: model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: v.ClusterName}, Service: v.ServiceName}, Kind: kind}
		out[key] = v
	}
	return out, rows.Err()
}

// DeleteInvestigationsBefore removes settled (done, failed, skipped) investigations finished before t, and never one
// that finished less than 48 hours before now, whatever t is. A requested or running row is never removed here. Their
// evidence and incident links cascade. The finished_at index serves the query.
func (s *Store) DeleteInvestigationsBefore(ctx context.Context, t, now time.Time) (int64, error) {
	if floor := now.Add(-minInvestigationRetention); t.After(floor) {
		t = floor
	}
	tag, err := s.Pool.Exec(ctx, `delete from agent_investigations
		where state in ('done', 'failed', 'skipped') and finished_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete investigations: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAgentRunsBefore removes finished runs that started before both cutoff and monthStart. A run that is running,
// or finished but not yet audited (the reconciler still reads it), and every run of the current month stay, so the
// month's spend never shrinks. The month start is moved back an hour, so a run at the boundary stays. A zero
// monthStart deletes nothing.
func (s *Store) DeleteAgentRunsBefore(ctx context.Context, cutoff, monthStart time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from agent_runs
		where state <> 'running' and audited_at is not null and started_at < $1 and started_at < $2`, cutoff, monthStart.Add(-time.Hour))
	if err != nil {
		return 0, fmt.Errorf("delete agent runs: %w", err)
	}
	return tag.RowsAffected(), nil
}
