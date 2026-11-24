package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func TestGetAgentConversation_OtherUsersIsNotFound(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	alice, _ := st.CreateUser(ctx, "alice", "h", false, false)
	bob, _ := st.CreateUser(ctx, "bob", "h", false, false)
	c, err := st.CreateAgentConversation(ctx, alice.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAgentConversation(ctx, bob.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get as other user = %v, want ErrNotFound", err)
	}
	if err := st.DeleteAgentConversation(ctx, bob.ID, c.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete as other user = %v, want ErrNotFound", err)
	}
	if _, err := st.GetAgentConversation(ctx, alice.ID, c.ID); err != nil {
		t.Fatalf("get as owner: %v", err)
	}
	if _, err := st.AppendAgentMessage(ctx, store.AgentMessage{ConversationID: c.ID, Role: "user", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendAgentMessage(ctx, store.AgentMessage{ConversationID: c.ID, Role: "assistant", Text: "yo", Tools: []string{"list_services"}, Interrupted: true}); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListAgentMessages(ctx, c.ID)
	if err != nil || len(msgs) != 2 || msgs[1].Text != "yo" || !msgs[1].Interrupted || len(msgs[1].Tools) != 1 {
		t.Fatalf("messages = %+v, %v", msgs, err)
	}
}

func TestDeleteAgentConversationsBefore(t *testing.T) {
	st := storetest.Open(t)
	ctx := t.Context()
	u, _ := st.CreateUser(ctx, "alice", "h", false, false)
	oldC, _ := st.CreateAgentConversation(ctx, u.ID, "old")
	newC, _ := st.CreateAgentConversation(ctx, u.ID, "new")
	if _, err := st.Pool.Exec(ctx, `update agent_conversations set updated_at = now() - interval '40 days' where id = $1`, oldC.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AppendAgentMessage(ctx, store.AgentMessage{ConversationID: oldC.ID, Role: "user", Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool.Exec(ctx, `update agent_conversations set updated_at = now() - interval '40 days' where id = $1`, oldC.ID); err != nil {
		t.Fatal(err)
	}
	n, err := st.DeleteAgentConversationsBefore(ctx, time.Now().Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("deleted %d, %v; want 1", n, err)
	}
	list, err := st.ListAgentConversations(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].ID != newC.ID {
		t.Fatalf("remaining = %+v, %v", list, err)
	}
	var msgs int
	if err := st.Pool.QueryRow(ctx, `select count(*) from agent_messages`).Scan(&msgs); err != nil || msgs != 0 {
		t.Fatalf("messages left = %d, %v", msgs, err)
	}
}
