package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// OffHoursSchedule selects services by one exact AWS tag and says when they are awake.
// Awake is 168 characters of 0/1, index day*24+hour with Monday = day 0.
type OffHoursSchedule struct {
	ID                                int64
	Name                              string
	Enabled                           bool
	TagKey, TagValue, Timezone, Awake string
	Version                           int
	UpdatedAt                         time.Time
	UpdatedBy                         *int64
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

const offHoursScheduleColumns = `id, name, enabled, tag_key, tag_value, timezone, awake, version, updated_at, updated_by`

func scanOffHoursSchedule(row pgx.Row) (OffHoursSchedule, error) {
	var sc OffHoursSchedule
	err := row.Scan(&sc.ID, &sc.Name, &sc.Enabled, &sc.TagKey, &sc.TagValue, &sc.Timezone, &sc.Awake, &sc.Version, &sc.UpdatedAt, &sc.UpdatedBy)
	return sc, err
}

// ListOffHoursSchedules returns every schedule ordered by name.
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
	return out, rows.Err()
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
	return sc, nil
}

// CreateOffHoursSchedule inserts a schedule at version 1. A duplicate name or tag selector returns ErrConflict.
// A zero by records no user.
func (s *Store) CreateOffHoursSchedule(ctx context.Context, sc OffHoursSchedule, by int64) (OffHoursSchedule, error) {
	created, err := scanOffHoursSchedule(s.Pool.QueryRow(ctx,
		`insert into offhours_schedules (name, enabled, tag_key, tag_value, timezone, awake, created_by, updated_by)
		 values ($1, $2, $3, $4, $5, $6, nullif($7, 0::bigint), nullif($7, 0::bigint))
		 returning `+offHoursScheduleColumns,
		sc.Name, sc.Enabled, sc.TagKey, sc.TagValue, sc.Timezone, sc.Awake, by))
	if isUniqueViolation(err) {
		return OffHoursSchedule{}, ErrConflict
	}
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("create off-hours schedule: %w", err)
	}
	return created, nil
}

// UpdateOffHoursSchedule replaces a schedule's fields when sc.Version is still current and bumps the version.
// A stale version or a duplicate name/selector returns ErrConflict; a missing schedule returns ErrNotFound.
func (s *Store) UpdateOffHoursSchedule(ctx context.Context, sc OffHoursSchedule, by int64) (OffHoursSchedule, error) {
	updated, err := scanOffHoursSchedule(s.Pool.QueryRow(ctx,
		`update offhours_schedules set name = $3, enabled = $4, tag_key = $5, tag_value = $6, timezone = $7, awake = $8,
		   version = version + 1, updated_at = now(), updated_by = nullif($9, 0::bigint)
		 where id = $1 and version = $2
		 returning `+offHoursScheduleColumns,
		sc.ID, sc.Version, sc.Name, sc.Enabled, sc.TagKey, sc.TagValue, sc.Timezone, sc.Awake, by))
	if isUniqueViolation(err) {
		return OffHoursSchedule{}, ErrConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if _, getErr := s.GetOffHoursSchedule(ctx, sc.ID); getErr != nil {
			return OffHoursSchedule{}, getErr
		}
		return OffHoursSchedule{}, ErrConflict
	}
	if err != nil {
		return OffHoursSchedule{}, fmt.Errorf("update off-hours schedule: %w", err)
	}
	return updated, nil
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

// OffHoursMatches returns the ACTIVE services whose AWS service tags contain exactly tagKey=tagValue
// (case-sensitive), with the facts needed to price them and their effective Harbormaster tag.
func (s *Store) OffHoursMatches(ctx context.Context, tagKey, tagValue string) ([]OffHoursMatch, error) {
	v := visibilityFor("s", Visibility{}, 3)
	rows, err := s.Pool.Query(ctx,
		`select s.account_id, s.region, s.cluster_name, s.service_name, coalesce(s.desired, 0), `+v.Effective+`,
		   (select name from tags where id = `+v.Effective+`), s.raw -> 'Tags', coalesce(s.launch_type, ''),
		   coalesce(s.raw #>> '{CapacityProviderStrategy,0,CapacityProvider}', ''),
		   coalesce(td.cpu, ''), coalesce(td.memory, ''), coalesce(td.raw #>> '{RuntimePlatform,CpuArchitecture}', '')
		 from services s`+v.Joins+`
		 left join task_definitions td on td.account_id = s.account_id and td.region = s.region and td.arn = s.task_definition_arn
		 where s.status = 'ACTIVE'
		   and (s.raw -> 'Tags' @> jsonb_build_array(jsonb_build_object('Key', $1::text, 'Value', $2::text))
		     -- An unset tag value is marshalled as null; it is the same as the empty value.
		     or ($2::text = '' and s.raw -> 'Tags' @> jsonb_build_array(jsonb_build_object('Key', $1::text, 'Value', null::text))))
		 order by s.account_id, s.region, s.cluster_name, s.service_name`, tagKey, tagValue)
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
