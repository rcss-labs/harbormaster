package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// AgentRunCosts returns the settled cost in micro-dollars of each given run that still exists. A run that retention
// has deleted, or one that was never recorded, is absent from the map.
func (s *Store) AgentRunCosts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	out := map[uuid.UUID]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `select id, cost_micro_usd from agent_runs where id = any($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("agent run costs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var cost int64
		if err := rows.Scan(&id, &cost); err != nil {
			return nil, fmt.Errorf("agent run costs: %w", err)
		}
		out[id] = cost
	}
	return out, rows.Err()
}
