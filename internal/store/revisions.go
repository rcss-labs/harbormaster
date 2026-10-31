package store

import (
	"context"
	"fmt"
	"time"
)

// RecordRevision notes which user registered a task definition revision. The
// first record for an ARN wins; later calls for the same ARN are ignored.
func (s *Store) RecordRevision(ctx context.Context, taskDefinitionARN string, userID int64, at time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`insert into revision_records (task_definition_arn, registered_by_user_id, registered_at)
		 values ($1, $2, $3)
		 on conflict do nothing`,
		taskDefinitionARN, userID, at)
	if err != nil {
		return fmt.Errorf("record revision: %w", err)
	}
	return nil
}

// RevisionRegistrars maps each given task definition ARN to the username that
// registered it. ARNs this instance never recorded are absent from the result.
func (s *Store) RevisionRegistrars(ctx context.Context, arns []string) (map[string]string, error) {
	registrars := map[string]string{}
	if len(arns) == 0 {
		return registrars, nil
	}
	rows, err := s.Pool.Query(ctx,
		`select r.task_definition_arn, u.username
		 from revision_records r
		 join users u on u.id = r.registered_by_user_id
		 where r.task_definition_arn = any($1)`,
		arns)
	if err != nil {
		return nil, fmt.Errorf("revision registrars: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var arn, username string
		if err := rows.Scan(&arn, &username); err != nil {
			return nil, fmt.Errorf("scan revision registrar: %w", err)
		}
		registrars[arn] = username
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("revision registrars: %w", err)
	}
	return registrars, nil
}
