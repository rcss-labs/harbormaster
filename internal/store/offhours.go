package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// Selector kinds of an off-hours schedule.
const (
	OffHoursSelectorAWS          = "aws"
	OffHoursSelectorHarbormaster = "harbormaster"
)

// OffHoursSelector is one tag a schedule selects services by. An "aws" selector matches the exact AWS service tag
// Key=Value (an empty Value also matches an unset one); a "harbormaster" selector matches the services whose
// effective Harbormaster tag is TagID.
type OffHoursSelector struct {
	Kind       string
	Key, Value string
	TagID      authz.TagID
}

// OffHoursSchedule selects services by a list of tags (a service matching any of them is selected, unless it is
// excluded) and says when they are awake. Awake is 168 characters of 0/1, index day*24+hour with Monday = day 0.
type OffHoursSchedule struct {
	ID              int64
	Name            string
	Enabled         bool
	Timezone, Awake string
	Selectors       []OffHoursSelector
	Exclusions      []model.ServiceKey
	Version         int
	UpdatedAt       time.Time
	UpdatedBy       *int64
}

// OffHoursEnrolment is the capacity saved from one service so it can be restored. A nil ScheduleID means the service
// is leaving: the reconciler restores it and then deletes the row.
type OffHoursEnrolment struct {
	Key                                      model.ServiceKey
	ScheduleID                               *int64
	SavedMin, SavedMax                       int
	SavedDesired                             *int
	RegisteredTarget                         bool
	AwakeTasks                               int
	State, BlockedReason, LastError          string
	LastAppliedAt, LastCheckedAt, WokenUntil *time.Time
	WokenBy                                  *int64
}

// OffHoursMatch is one polled service carrying the tag, with the facts needed to price it.
type OffHoursMatch struct {
	Key                                                     model.ServiceKey
	Desired                                                 int
	EffectiveTagID                                          authz.TagID
	EffectiveTagName                                        string
	AWSTags                                                 map[string]string
	LaunchType, CapacityProvider, CPU, Memory, Architecture string
}

// OffHoursTagValue is one AWS tag key/value seen on active services and how many carry it.
type OffHoursTagValue struct {
	Key, Value string
	Services   int
}

// OffHoursTagCount is a Harbormaster tag with the number of ACTIVE services whose effective tag it is.
type OffHoursTagCount struct {
	ID          authz.TagID
	Name, Color string
	Services    int
}

const offHoursScheduleColumns = `id, name, enabled, timezone, awake, version, updated_at, updated_by`

func scanOffHoursSchedule(row pgx.Row) (OffHoursSchedule, error) {
	var sc OffHoursSchedule
	err := row.Scan(&sc.ID, &sc.Name, &sc.Enabled, &sc.Timezone, &sc.Awake, &sc.Version, &sc.UpdatedAt, &sc.UpdatedBy)
	return sc, err
}

// offHoursQuerier is the part of the pool and of a transaction that the child loader needs.
type offHoursQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadOffHoursChildren fills Selectors and Exclusions of every schedule with two queries, whatever the count.
func loadOffHoursChildren(ctx context.Context, q offHoursQuerier, schedules []OffHoursSchedule) error {
	if len(schedules) == 0 {
		return nil
	}
	ids := make([]int64, len(schedules))
	index := make(map[int64]int, len(schedules))
	for i := range schedules {
		ids[i], index[schedules[i].ID] = schedules[i].ID, i
		schedules[i].Selectors, schedules[i].Exclusions = []OffHoursSelector{}, []model.ServiceKey{}
	}
	rows, err := q.Query(ctx, `select schedule_id, kind, coalesce(aws_key, ''), coalesce(aws_value, ''), coalesce(tag_id, 0)
		from offhours_schedule_tags where schedule_id = any($1)
		order by schedule_id, kind, aws_key, aws_value, tag_id`, ids)
	if err != nil {
		return fmt.Errorf("load off-hours selectors: %w", err)
	}
	for rows.Next() {
		var id int64
		var sel OffHoursSelector
		if err := rows.Scan(&id, &sel.Kind, &sel.Key, &sel.Value, &sel.TagID); err != nil {
			rows.Close()
			return fmt.Errorf("scan off-hours selector: %w", err)
		}
		sc := &schedules[index[id]]
		sc.Selectors = append(sc.Selectors, sel)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load off-hours selectors: %w", err)
	}
	rows, err = q.Query(ctx, `select schedule_id, account_id, region, cluster_name, service_name
		from offhours_schedule_exclusions where schedule_id = any($1)
		order by schedule_id, account_id, region, cluster_name, service_name`, ids)
	if err != nil {
		return fmt.Errorf("load off-hours exclusions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key model.ServiceKey
		if err := rows.Scan(&id, &key.AccountID, &key.Region, &key.Cluster, &key.Service); err != nil {
			return fmt.Errorf("scan off-hours exclusion: %w", err)
		}
		sc := &schedules[index[id]]
		sc.Exclusions = append(sc.Exclusions, key)
	}
	return rows.Err()
}

// replaceOffHoursChildren rewrites the selectors and exclusions of a schedule inside tx.
func replaceOffHoursChildren(ctx context.Context, tx pgx.Tx, scheduleID int64, sc OffHoursSchedule) error {
	if _, err := tx.Exec(ctx, `delete from offhours_schedule_tags where schedule_id = $1`, scheduleID); err != nil {
		return fmt.Errorf("clear off-hours selectors: %w", err)
	}
	if _, err := tx.Exec(ctx, `delete from offhours_schedule_exclusions where schedule_id = $1`, scheduleID); err != nil {
		return fmt.Errorf("clear off-hours exclusions: %w", err)
	}
	for _, sel := range sc.Selectors {
		var err error
		if sel.Kind == OffHoursSelectorHarbormaster {
			_, err = tx.Exec(ctx, `insert into offhours_schedule_tags (schedule_id, kind, tag_id) values ($1, 'harbormaster', $2)`,
				scheduleID, int64(sel.TagID))
		} else {
			_, err = tx.Exec(ctx, `insert into offhours_schedule_tags (schedule_id, kind, aws_key, aws_value) values ($1, 'aws', $2, $3)`,
				scheduleID, sel.Key, sel.Value)
		}
		if err != nil {
			return err
		}
	}
	if len(sc.Exclusions) == 0 {
		return nil
	}
	accounts, regions, clusters, services := []string{}, []string{}, []string{}, []string{}
	for _, k := range sc.Exclusions {
		accounts, regions = append(accounts, k.AccountID), append(regions, k.Region)
		clusters, services = append(clusters, k.Cluster), append(services, k.Service)
	}
	_, err := tx.Exec(ctx, `insert into offhours_schedule_exclusions (schedule_id, account_id, region, cluster_name, service_name)
		select $1, a, r, c, sv from unnest($2::text[], $3::text[], $4::text[], $5::text[]) as x(a, r, c, sv)
		on conflict do nothing`, scheduleID, accounts, regions, clusters, services)
	return err
}

// SplitExcluded divides matches into the services a schedule keeps and the ones its exclusions remove, preserving order.
func SplitExcluded(matches []OffHoursMatch, excluded []model.ServiceKey) (kept, removed []OffHoursMatch) {
	skip := make(map[model.ServiceKey]bool, len(excluded))
	for _, k := range excluded {
		skip[k] = true
	}
	kept, removed = []OffHoursMatch{}, []OffHoursMatch{}
	for _, m := range matches {
		if skip[m.Key] {
			removed = append(removed, m)
		} else {
			kept = append(kept, m)
		}
	}
	return kept, removed
}

// ListOffHoursSchedules returns every schedule ordered by name, with selectors and exclusions.
func (s *Store) ListOffHoursSchedules(ctx context.Context) ([]OffHoursSchedule, error) {
	rows, err := s.Pool.Query(ctx, `select `+offHoursScheduleColumns+` from offhours_schedules order by name`)
	if err != nil {
		return nil, fmt.Errorf("list off-hours schedules: %w", err)
	}
	defer rows.Close()
	out := []OffHoursSchedule{}
	for rows.Next() {
		sc, err := scanOffHoursSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan off-hours schedule: %w", err)
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := loadOffHoursChildren(ctx, s.Pool, out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetOffHoursSchedule returns one schedule, or ErrNotFound.
func (s *Store) GetOffHoursSchedule(ctx context.Context, id int64) (OffHoursSchedule, error) {
	sc, err := scanOffHoursSchedule(s.Pool.QueryRow(ctx, `select `+offHoursScheduleColumns+` from offhours_schedules where id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return OffHoursSchedule{}, ErrNotFound
	}
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("get off-hours schedule: %w", err)
	}
	one := []OffHoursSchedule{sc}
	if err := loadOffHoursChildren(ctx, s.Pool, one); err != nil {
		return OffHoursSchedule{}, err
	}
	return one[0], nil
}

// CreateOffHoursSchedule inserts a schedule at version 1 with its selectors and exclusions in one transaction.
// A duplicate name returns ErrConflict. A zero by records no user.
func (s *Store) CreateOffHoursSchedule(ctx context.Context, sc OffHoursSchedule, by int64) (OffHoursSchedule, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("create off-hours schedule: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	created, err := scanOffHoursSchedule(tx.QueryRow(ctx,
		`insert into offhours_schedules (name, enabled, timezone, awake, created_by, updated_by)
		 values ($1, $2, $3, $4, nullif($5, 0::bigint), nullif($5, 0::bigint))
		 returning `+offHoursScheduleColumns,
		sc.Name, sc.Enabled, sc.Timezone, sc.Awake, by))
	if isUniqueViolation(err) {
		return OffHoursSchedule{}, ErrConflict
	}
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("create off-hours schedule: %w", err)
	}
	if err := replaceOffHoursChildren(ctx, tx, created.ID, sc); err != nil {
		return OffHoursSchedule{}, fmt.Errorf("create off-hours schedule: %w", err)
	}
	one := []OffHoursSchedule{created}
	if err := loadOffHoursChildren(ctx, tx, one); err != nil {
		return OffHoursSchedule{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OffHoursSchedule{}, fmt.Errorf("create off-hours schedule: %w", err)
	}
	return one[0], nil
}

// UpdateOffHoursSchedule replaces a schedule's fields, selectors and exclusions when sc.Version is still current and
// bumps the version, all in one transaction. A stale version or a duplicate name returns ErrConflict; a missing
// schedule returns ErrNotFound.
func (s *Store) UpdateOffHoursSchedule(ctx context.Context, sc OffHoursSchedule, by int64) (OffHoursSchedule, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("update off-hours schedule: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	updated, err := scanOffHoursSchedule(tx.QueryRow(ctx,
		`update offhours_schedules set name = $3, enabled = $4, timezone = $5, awake = $6,
		   version = version + 1, updated_at = now(), updated_by = nullif($7, 0::bigint)
		 where id = $1 and version = $2
		 returning `+offHoursScheduleColumns,
		sc.ID, sc.Version, sc.Name, sc.Enabled, sc.Timezone, sc.Awake, by))
	if isUniqueViolation(err) {
		return OffHoursSchedule{}, ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		tx.Rollback(ctx) //nolint:errcheck // nothing was written
		if _, getErr := s.GetOffHoursSchedule(ctx, sc.ID); getErr != nil {
			return OffHoursSchedule{}, getErr
		}
		return OffHoursSchedule{}, ErrConflict
	}
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("update off-hours schedule: %w", err)
	}
	if err := replaceOffHoursChildren(ctx, tx, updated.ID, sc); err != nil {
		return OffHoursSchedule{}, fmt.Errorf("update off-hours schedule: %w", err)
	}
	one := []OffHoursSchedule{updated}
	if err := loadOffHoursChildren(ctx, tx, one); err != nil {
		return OffHoursSchedule{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OffHoursSchedule{}, fmt.Errorf("update off-hours schedule: %w", err)
	}
	return one[0], nil
}

// DeleteOffHoursSchedule removes a schedule. Its enrolments stay with a NULL schedule_id so the reconciler can restore
// the saved capacity. Returns ErrNotFound if absent.
func (s *Store) DeleteOffHoursSchedule(ctx context.Context, id int64) error {
	tag, err := s.Pool.Exec(ctx, `delete from offhours_schedules where id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete off-hours schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// OffHoursMatches returns the ACTIVE services that carry ANY of the selectors, each once, with the facts needed to
// price them and their effective Harbormaster tag. AWS tags match case-sensitively. No selectors match nothing.
func (s *Store) OffHoursMatches(ctx context.Context, selectors []OffHoursSelector) ([]OffHoursMatch, error) {
	if len(selectors) == 0 {
		return []OffHoursMatch{}, nil
	}
	v := visibilityFor("s", Visibility{}, 1)
	var args []any
	var conditions []string
	var tagIDs []int64
	for _, sel := range selectors {
		if sel.Kind == OffHoursSelectorHarbormaster {
			tagIDs = append(tagIDs, int64(sel.TagID))
			continue
		}
		args = append(args, sel.Key, sel.Value)
		key, value := len(args)-1, len(args)
		// An unset tag value is marshalled as null; it is the same as the empty value.
		conditions = append(conditions, fmt.Sprintf(
			`(s.raw -> 'Tags' @> jsonb_build_array(jsonb_build_object('Key', $%[1]d::text, 'Value', $%[2]d::text))`+
				` or ($%[2]d::text = '' and s.raw -> 'Tags' @> jsonb_build_array(jsonb_build_object('Key', $%[1]d::text, 'Value', null::text))))`,
			key, value))
	}
	if len(tagIDs) > 0 {
		args = append(args, tagIDs)
		conditions = append(conditions, fmt.Sprintf(`%s = any($%d::bigint[])`, v.Effective, len(args)))
	}
	rows, err := s.Pool.Query(ctx,
		`select s.account_id, s.region, s.cluster_name, s.service_name, coalesce(s.desired, 0), `+v.Effective+`,
		   (select name from tags where id = `+v.Effective+`), s.raw -> 'Tags', coalesce(s.launch_type, ''),
		   coalesce(s.raw #>> '{CapacityProviderStrategy,0,CapacityProvider}', ''),
		   coalesce(td.cpu, ''), coalesce(td.memory, ''), coalesce(td.raw #>> '{RuntimePlatform,CpuArchitecture}', '')
		 from services s`+v.Joins+`
		 left join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 where s.status = 'ACTIVE' and (`+strings.Join(conditions, " or ")+`)
		 order by s.account_id, s.region, s.cluster_name, s.service_name`, args...)
	if err != nil {
		return nil, fmt.Errorf("off-hours matches: %w", err)
	}
	defer rows.Close()
	out := []OffHoursMatch{}
	for rows.Next() {
		var m OffHoursMatch
		var tagsJSON []byte
		if err := rows.Scan(&m.Key.AccountID, &m.Key.Region, &m.Key.Cluster, &m.Key.Service, &m.Desired, &m.EffectiveTagID,
			&m.EffectiveTagName, &tagsJSON, &m.LaunchType, &m.CapacityProvider, &m.CPU, &m.Memory, &m.Architecture); err != nil {
			return nil, fmt.Errorf("scan off-hours match: %w", err)
		}
		var tags []struct{ Key, Value string }
		if err := json.Unmarshal(tagsJSON, &tags); err != nil {
			return nil, fmt.Errorf("decode tags of %s: %w", m.Key.Service, err)
		}
		m.AWSTags = make(map[string]string, len(tags))
		for _, t := range tags {
			m.AWSTags[t.Key] = t.Value
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// OffHoursHarbormasterTags lists every Harbormaster tag with the number of ACTIVE services whose effective tag it is,
// zero counts included, ordered by id.
func (s *Store) OffHoursHarbormasterTags(ctx context.Context) ([]OffHoursTagCount, error) {
	v := visibilityFor("s", Visibility{}, 1)
	rows, err := s.Pool.Query(ctx,
		`select t.id, t.name, t.color,
		   (select count(*) from services s`+v.Joins+` where s.status = 'ACTIVE' and `+v.Effective+` = t.id)
		 from tags t order by t.id`)
	if err != nil {
		return nil, fmt.Errorf("off-hours harbormaster tags: %w", err)
	}
	defer rows.Close()
	out := []OffHoursTagCount{}
	for rows.Next() {
		var tc OffHoursTagCount
		if err := rows.Scan(&tc.ID, &tc.Name, &tc.Color, &tc.Services); err != nil {
			return nil, fmt.Errorf("scan off-hours harbormaster tag: %w", err)
		}
		out = append(out, tc)
	}
	return out, rows.Err()
}

// OffHoursTagValues lists every AWS tag key/value on ACTIVE services with the number of services carrying it.
func (s *Store) OffHoursTagValues(ctx context.Context) ([]OffHoursTagValue, error) {
	rows, err := s.Pool.Query(ctx,
		`select t ->> 'Key', coalesce(t ->> 'Value', ''),
		   count(distinct (s.account_id, s.region, s.cluster_name, s.service_name))
		 from services s, jsonb_array_elements(case when jsonb_typeof(s.raw -> 'Tags') = 'array' then s.raw -> 'Tags' else '[]'::jsonb end) t
		 where s.status = 'ACTIVE' and t ? 'Key'
		 group by 1, 2 order by 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("off-hours tag values: %w", err)
	}
	defer rows.Close()
	out := []OffHoursTagValue{}
	for rows.Next() {
		var tv OffHoursTagValue
		if err := rows.Scan(&tv.Key, &tv.Value, &tv.Services); err != nil {
			return nil, fmt.Errorf("scan off-hours tag value: %w", err)
		}
		out = append(out, tv)
	}
	return out, rows.Err()
}

const offHoursEnrolmentColumns = `account_id, region, cluster_name, service_name, schedule_id, saved_min, saved_max, saved_desired,
  registered_target, awake_tasks, state, coalesce(blocked_reason, ''), coalesce(last_error, ''),
  last_applied_at, last_checked_at, woken_until, woken_by`

func scanOffHoursEnrolment(row pgx.Row) (OffHoursEnrolment, error) {
	var e OffHoursEnrolment
	err := row.Scan(&e.Key.AccountID, &e.Key.Region, &e.Key.Cluster, &e.Key.Service, &e.ScheduleID, &e.SavedMin, &e.SavedMax,
		&e.SavedDesired, &e.RegisteredTarget, &e.AwakeTasks, &e.State, &e.BlockedReason, &e.LastError,
		&e.LastAppliedAt, &e.LastCheckedAt, &e.WokenUntil, &e.WokenBy)
	return e, err
}

// ListOffHoursEnrolments returns every enrolment, including those that are leaving.
func (s *Store) ListOffHoursEnrolments(ctx context.Context) ([]OffHoursEnrolment, error) {
	rows, err := s.Pool.Query(ctx, `select `+offHoursEnrolmentColumns+` from offhours_enrolments
		order by account_id, region, cluster_name, service_name`)
	if err != nil {
		return nil, fmt.Errorf("list off-hours enrolments: %w", err)
	}
	defer rows.Close()
	out := []OffHoursEnrolment{}
	for rows.Next() {
		e, err := scanOffHoursEnrolment(rows)
		if err != nil {
			return nil, fmt.Errorf("scan off-hours enrolment: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const offHoursEnrolmentByKey = ` from offhours_enrolments
	where account_id = $1 and region = $2 and cluster_name = $3 and service_name = $4`

// GetOffHoursEnrolment returns one enrolment, or ErrNotFound.
func (s *Store) GetOffHoursEnrolment(ctx context.Context, k model.ServiceKey) (OffHoursEnrolment, error) {
	e, err := scanOffHoursEnrolment(s.Pool.QueryRow(ctx, `select `+offHoursEnrolmentColumns+offHoursEnrolmentByKey,
		k.AccountID, k.Region, k.Cluster, k.Service))
	if errors.Is(err, pgx.ErrNoRows) {
		return OffHoursEnrolment{}, ErrNotFound
	}
	if err != nil {
		return OffHoursEnrolment{}, fmt.Errorf("get off-hours enrolment: %w", err)
	}
	return e, nil
}

// LockOffHoursEnrolment reads the enrolment with "for update" inside tx, so a second transaction waits until this one
// ends. Returns ErrNotFound when the service has no enrolment.
func (s *Store) LockOffHoursEnrolment(ctx context.Context, tx Tx, k model.ServiceKey) (OffHoursEnrolment, error) {
	e, err := scanOffHoursEnrolment(tx.tx.QueryRow(ctx, `select `+offHoursEnrolmentColumns+offHoursEnrolmentByKey+` for update`,
		k.AccountID, k.Region, k.Cluster, k.Service))
	if errors.Is(err, pgx.ErrNoRows) {
		return OffHoursEnrolment{}, ErrNotFound
	}
	if err != nil {
		return OffHoursEnrolment{}, fmt.Errorf("lock off-hours enrolment: %w", err)
	}
	return e, nil
}

// UpsertOffHoursEnrolment inserts or replaces the enrolment of e.Key inside tx. enrolled_at is kept on update.
func (s *Store) UpsertOffHoursEnrolment(ctx context.Context, tx Tx, e OffHoursEnrolment) error {
	_, err := tx.tx.Exec(ctx,
		`insert into offhours_enrolments (account_id, region, cluster_name, service_name, schedule_id, saved_min, saved_max,
		   saved_desired, registered_target, awake_tasks, state, blocked_reason, last_error, last_applied_at,
		   last_checked_at, woken_until, woken_by)
		 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		 on conflict (account_id, region, cluster_name, service_name) do update set
		   schedule_id = excluded.schedule_id, saved_min = excluded.saved_min, saved_max = excluded.saved_max,
		   saved_desired = excluded.saved_desired, registered_target = excluded.registered_target,
		   awake_tasks = excluded.awake_tasks, state = excluded.state, blocked_reason = excluded.blocked_reason,
		   last_error = excluded.last_error, last_applied_at = excluded.last_applied_at,
		   last_checked_at = excluded.last_checked_at, woken_until = excluded.woken_until, woken_by = excluded.woken_by`,
		e.Key.AccountID, e.Key.Region, e.Key.Cluster, e.Key.Service, e.ScheduleID, e.SavedMin, e.SavedMax, e.SavedDesired,
		e.RegisteredTarget, e.AwakeTasks, e.State, nullString(e.BlockedReason), nullString(e.LastError),
		e.LastAppliedAt, e.LastCheckedAt, e.WokenUntil, e.WokenBy)
	if err != nil {
		return fmt.Errorf("upsert off-hours enrolment: %w", err)
	}
	return nil
}

// DeleteOffHoursEnrolment removes the enrolment inside tx; a missing row is not an error.
func (s *Store) DeleteOffHoursEnrolment(ctx context.Context, tx Tx, k model.ServiceKey) error {
	_, err := tx.tx.Exec(ctx, `delete`+offHoursEnrolmentByKey, k.AccountID, k.Region, k.Cluster, k.Service)
	if err != nil {
		return fmt.Errorf("delete off-hours enrolment: %w", err)
	}
	return nil
}

// LockOffHoursService takes a transaction-scoped advisory lock keyed by the service, so two reconcilers (a stale
// ex-leader and the new leader) cannot both enrol a service that has no row yet to lock. It waits for the holder.
func (s *Store) LockOffHoursService(ctx context.Context, tx Tx, k model.ServiceKey) error {
	_, err := tx.tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`,
		"offhours/"+k.AccountID+"/"+k.Region+"/"+k.Cluster+"/"+k.Service)
	if err != nil {
		return fmt.Errorf("lock off-hours service: %w", err)
	}
	return nil
}
