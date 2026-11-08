package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// MaxSavedLogSearches caps how many saved log searches one user may keep.
const MaxSavedLogSearches = 50

// ErrSavedLimit is returned when saving a new name would exceed MaxSavedLogSearches.
var ErrSavedLimit = errors.New("saved log search limit reached")

// SavedLogSearch is a named log query a user can re-run. From and To are set only for the 'custom' range.
type SavedLogSearch struct {
	ID         int64
	UserID     int64
	Name       string
	Query      string
	ServiceIDs []string
	Range      string
	From       *time.Time
	To         *time.Time
	UpdatedAt  time.Time
}

const savedLogSearchColumns = `id, user_id, name, query, service_ids, range, range_from, range_to, updated_at`

func scanSavedLogSearch(row pgx.Row) (SavedLogSearch, error) {
	var q SavedLogSearch
	var serviceIDsJSON []byte
	err := row.Scan(&q.ID, &q.UserID, &q.Name, &q.Query, &serviceIDsJSON, &q.Range, &q.From, &q.To, &q.UpdatedAt)
	if err != nil {
		return SavedLogSearch{}, err
	}
	if err := json.Unmarshal(serviceIDsJSON, &q.ServiceIDs); err != nil {
		return SavedLogSearch{}, fmt.Errorf("decode service ids: %w", err)
	}
	return q, nil
}

// ListSavedLogSearches returns the user's saved log searches ordered by name.
func (s *Store) ListSavedLogSearches(ctx context.Context, userID int64) ([]SavedLogSearch, error) {
	rows, err := s.Pool.Query(ctx,
		`select `+savedLogSearchColumns+` from log_search_saved where user_id = $1 order by name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list saved log searches: %w", err)
	}
	defer rows.Close()
	searches := []SavedLogSearch{}
	for rows.Next() {
		q, err := scanSavedLogSearch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan saved log search: %w", err)
		}
		searches = append(searches, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list saved log searches: %w", err)
	}
	return searches, nil
}

// SaveLogSearch inserts a saved log search, or replaces the query and scope of the user's search with the same name.
// A new name is refused with ErrSavedLimit once the user holds MaxSavedLogSearches.
func (s *Store) SaveLogSearch(ctx context.Context, q SavedLogSearch) (SavedLogSearch, error) {
	serviceIDs := q.ServiceIDs
	if serviceIDs == nil {
		serviceIDs = []string{}
	}
	serviceIDsJSON, err := json.Marshal(serviceIDs)
	if err != nil {
		return SavedLogSearch{}, fmt.Errorf("encode service ids: %w", err)
	}

	var saved SavedLogSearch
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// Locking the user row serialises this user's saves, so two concurrent saves cannot both pass the count check.
		if _, err := tx.Exec(ctx, `select id from users where id = $1 for update`, q.UserID); err != nil {
			return fmt.Errorf("lock user: %w", err)
		}
		var nameTaken bool
		if err := tx.QueryRow(ctx,
			`select exists(select 1 from log_search_saved where user_id = $1 and name = $2)`,
			q.UserID, q.Name).Scan(&nameTaken); err != nil {
			return fmt.Errorf("check saved log search name: %w", err)
		}
		if !nameTaken {
			var count int
			if err := tx.QueryRow(ctx,
				`select count(*) from log_search_saved where user_id = $1`, q.UserID).Scan(&count); err != nil {
				return fmt.Errorf("count saved log searches: %w", err)
			}
			if count >= MaxSavedLogSearches {
				return ErrSavedLimit
			}
		}
		row := tx.QueryRow(ctx,
			`insert into log_search_saved (user_id, name, query, service_ids, range, range_from, range_to, updated_at)
			 values ($1, $2, $3, $4, $5, $6, $7, now())
			 on conflict (user_id, name) do update set
			   query = excluded.query,
			   service_ids = excluded.service_ids,
			   range = excluded.range,
			   range_from = excluded.range_from,
			   range_to = excluded.range_to,
			   updated_at = now()
			 returning `+savedLogSearchColumns,
			q.UserID, q.Name, q.Query, serviceIDsJSON, q.Range, q.From, q.To)
		inserted, scanErr := scanSavedLogSearch(row)
		if scanErr != nil {
			return fmt.Errorf("save log search: %w", scanErr)
		}
		saved = inserted
		return nil
	})
	if err != nil {
		return SavedLogSearch{}, err
	}
	return saved, nil
}

// DeleteSavedLogSearch removes one of the user's saved log searches, or returns ErrNotFound when it is not theirs.
func (s *Store) DeleteSavedLogSearch(ctx context.Context, userID, id int64) error {
	tag, err := s.Pool.Exec(ctx, `delete from log_search_saved where id = $1 and user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete saved log search: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
