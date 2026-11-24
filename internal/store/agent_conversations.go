package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AgentConversation is one user's chat thread.
type AgentConversation struct {
	ID        int64
	UserID    int64
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AgentMessage is one turn of a conversation. Tools lists tool names only: results are never stored.
type AgentMessage struct {
	ID             int64
	ConversationID int64
	Role           string // user | assistant
	Text           string
	Tools          []string
	Interrupted    bool
	CreatedAt      time.Time
}

const agentConversationColumns = `id, user_id, title, created_at, updated_at`

func scanAgentConversation(row pgx.Row) (AgentConversation, error) {
	var c AgentConversation
	err := row.Scan(&c.ID, &c.UserID, &c.Title, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentConversation{}, ErrNotFound
	}
	return c, err
}

// CreateAgentConversation starts an empty conversation for the user.
func (s *Store) CreateAgentConversation(ctx context.Context, userID int64, title string) (AgentConversation, error) {
	c, err := scanAgentConversation(s.Pool.QueryRow(ctx,
		`insert into agent_conversations (user_id, title) values ($1, $2) returning `+agentConversationColumns, userID, title))
	if err != nil {
		return AgentConversation{}, fmt.Errorf("create agent conversation: %w", err)
	}
	return c, nil
}

// ListAgentConversations returns the user's conversations, most recently active first.
func (s *Store) ListAgentConversations(ctx context.Context, userID int64) ([]AgentConversation, error) {
	rows, err := s.Pool.Query(ctx, `select `+agentConversationColumns+` from agent_conversations
		where user_id = $1 order by updated_at desc, id desc`, userID)
	if err != nil {
		return nil, fmt.Errorf("list agent conversations: %w", err)
	}
	defer rows.Close()
	var out []AgentConversation
	for rows.Next() {
		c, err := scanAgentConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent conversation: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetAgentConversation returns the conversation, or ErrNotFound when it does not exist or belongs to another user.
func (s *Store) GetAgentConversation(ctx context.Context, userID, id int64) (AgentConversation, error) {
	return scanAgentConversation(s.Pool.QueryRow(ctx,
		`select `+agentConversationColumns+` from agent_conversations where id = $1 and user_id = $2`, id, userID))
}

// DeleteAgentConversation removes the user's conversation and its messages; another user's is ErrNotFound.
func (s *Store) DeleteAgentConversation(ctx context.Context, userID, id int64) error {
	tag, err := s.Pool.Exec(ctx, `delete from agent_conversations where id = $1 and user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete agent conversation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAgentMessages returns a conversation's messages in order. Callers check ownership first.
func (s *Store) ListAgentMessages(ctx context.Context, conversationID int64) ([]AgentMessage, error) {
	rows, err := s.Pool.Query(ctx, `select id, conversation_id, role, text, tools, interrupted, created_at
		from agent_messages where conversation_id = $1 order by id`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list agent messages: %w", err)
	}
	defer rows.Close()
	var out []AgentMessage
	for rows.Next() {
		var m AgentMessage
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Role, &m.Text, &m.Tools, &m.Interrupted, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan agent message: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AppendAgentMessage stores a message and bumps the conversation's updated_at, which retention measures.
func (s *Store) AppendAgentMessage(ctx context.Context, m AgentMessage) (AgentMessage, error) {
	tools := m.Tools
	if tools == nil {
		tools = []string{}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return AgentMessage{}, fmt.Errorf("append agent message: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	err = tx.QueryRow(ctx, `insert into agent_messages (conversation_id, role, text, tools, interrupted)
		values ($1, $2, $3, $4, $5) returning id, created_at`,
		m.ConversationID, m.Role, m.Text, tools, m.Interrupted).Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return AgentMessage{}, fmt.Errorf("append agent message: %w", err)
	}
	if _, err := tx.Exec(ctx, `update agent_conversations set updated_at = now() where id = $1`, m.ConversationID); err != nil {
		return AgentMessage{}, fmt.Errorf("append agent message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentMessage{}, fmt.Errorf("append agent message: %w", err)
	}
	m.Tools = tools
	return m, nil
}

// DeleteAgentConversationsBefore removes conversations not active since t, messages included, and returns how many.
func (s *Store) DeleteAgentConversationsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `delete from agent_conversations where updated_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete agent conversations: %w", err)
	}
	return tag.RowsAffected(), nil
}
