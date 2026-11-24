package store

import (
	"context"
	"fmt"
)

// SetAgentConversationTitleIfEmpty names the user's conversation after its first message. A conversation that
// already has a title, or belongs to someone else, is left alone.
func (s *Store) SetAgentConversationTitleIfEmpty(ctx context.Context, userID, id int64, title string) error {
	if _, err := s.Pool.Exec(ctx, `update agent_conversations set title = $3 where id = $1 and user_id = $2 and title = ''`,
		id, userID, title); err != nil {
		return fmt.Errorf("set agent conversation title: %w", err)
	}
	return nil
}
