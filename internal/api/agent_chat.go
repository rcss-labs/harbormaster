package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	chatTextMaxChars    = 8000
	chatTitleMaxChars   = 60
	chatPagePathMax     = 512
	chatRoleName        = "chat"
	minRunStaleAfter    = 10 * time.Minute
	budgetExhaustedMsg  = "This month's AI allowance is used up. Ask an admin to raise it."
	agentUnavailableMsg = "The agent is unavailable. Try again shortly."
)

var (
	errChatBusy      = &APIError{Status: http.StatusConflict, Code: "chat_busy", Message: "You already have a question in progress. Wait for it to finish."}
	errNotConfigured = &APIError{Status: http.StatusConflict, Code: "agent_not_configured", Message: "No chat model is set up. Ask an admin to choose one."}
	errAgentDown     = &APIError{Status: http.StatusServiceUnavailable, Code: "agent_unavailable", Message: agentUnavailableMsg}
	errBudget        = &APIError{Status: http.StatusPaymentRequired, Code: "budget_exhausted", Message: budgetExhaustedMsg}
)

// chatUser answers 404 unless the caller is a global admin or holds the AI chat flag. A user who may not chat
// learns nothing of the routes.
func (s *Server) chatUser(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	user, _ := auth.UserFrom(r.Context())
	if !s.mayChat(r, user) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return store.User{}, false
	}
	return user, true
}

// mayChat reports whether the caller of r may use the chat: the agent is on and they are an admin or hold the flag.
func (s *Server) mayChat(r *http.Request, user store.User) bool {
	return s.Cfg.AgentEnabled() && (auth.SubjectFrom(r.Context()).Admin || user.AIChat)
}

// conversationID parses the {id} path value; anything else is a 404.
func conversationID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, store.ErrNotFound
	}
	return id, nil
}

// AgentStatusDTO is the body of GET /api/agent/status. AgentReachable is whether the agent's catalogue could be
// read just now; ModelConfigured comes from the stored settings and is only taken back when the agent answered and
// does not offer the chosen model, so an agent that is down does not look like a missing setting.
type AgentStatusDTO struct {
	Enabled         bool `json:"enabled"`
	CanChat         bool `json:"can_chat"`
	ModelConfigured bool `json:"model_configured"`
	BudgetExhausted bool `json:"budget_exhausted"`
	AgentReachable  bool `json:"agent_reachable"`
}

const (
	statusCatalogueTimeout = 5 * time.Second
	statusNegativeCache    = 10 * time.Second
)

// agentReach remembers for a short while that the agent could not be reached, so a page that polls the status does
// not queue behind a dead agent. It also holds the one-time warning about the run token lifetime.
type agentReach struct {
	mu        sync.Mutex
	downUntil time.Time
	ttlWarned sync.Once
}

func (a *agentReach) down(now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return now.Before(a.downUntil)
}

func (a *agentReach) markDown(until time.Time) {
	a.mu.Lock()
	a.downUntil = until
	a.mu.Unlock()
}

// agentStatus answers for any signed-in user; only a user who may chat is told more than that the agent is on.
func (s *Server) agentStatus(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	out := AgentStatusDTO{Enabled: s.Cfg.AgentEnabled(), CanChat: s.mayChat(r, user)}
	if !out.CanChat {
		writeJSON(w, http.StatusOK, out)
		return
	}
	settings, err := s.Store.GetAgentSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	ref, chosen := settings.RoleModels[chatRoleName]
	out.ModelConfigured = chosen

	var chosenModel agentclient.Model
	var cat agentclient.Catalogue
	if !s.agentReach.down(s.now()) {
		ctx, cancel := context.WithTimeout(r.Context(), statusCatalogueTimeout)
		var catErr error
		if s.Agent == nil {
			catErr = agentclient.ErrAgentUnavailable
		} else {
			cat, catErr = s.Agent.Catalogue(ctx)
		}
		cancel()
		if catErr != nil {
			if r.Context().Err() == nil {
				s.agentReach.markDown(s.now().Add(statusNegativeCache))
			}
		} else {
			out.AgentReachable = true
		}
	}
	var found bool
	if out.AgentReachable && chosen {
		chosenModel, found = findModel(cat, ref)
		out.ModelConfigured = found
	}

	settled, reserved, err := s.Store.AgentSpend(r.Context(), s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	// Exhausted means the next question would be refused: no room left for one run's reservation.
	needed := int64(0)
	if found {
		needed = chosenModel.MaxRunCostMicroUSD(cat.MaxRunTokens)
	}
	budget := settings.MonthlyBudgetMicroUSD
	out.BudgetExhausted = budget <= 0 || settled > budget || reserved > budget-settled || needed > budget-settled-reserved
	writeJSON(w, http.StatusOK, out)
}

func findModel(cat agentclient.Catalogue, ref store.AgentModelRef) (agentclient.Model, bool) {
	for _, p := range cat.Providers {
		if p.Provider != ref.Provider {
			continue
		}
		for _, m := range p.Models {
			if m.ID == ref.Model {
				return m, true
			}
		}
	}
	return agentclient.Model{}, false
}

func (s *Server) catalogue(r *http.Request) (agentclient.Catalogue, error) {
	if s.Agent == nil {
		return agentclient.Catalogue{}, agentclient.ErrAgentUnavailable
	}
	return s.Agent.Catalogue(r.Context())
}

type AgentConversationSummaryDTO struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentMessageDTO struct {
	ID          int64     `json:"id"`
	Role        string    `json:"role"`
	Text        string    `json:"text"`
	Tools       []string  `json:"tools"`
	Interrupted bool      `json:"interrupted"`
	CreatedAt   time.Time `json:"created_at"`
}

type AgentConversationDTO struct {
	ID       int64             `json:"id"`
	Title    string            `json:"title"`
	Messages []AgentMessageDTO `json:"messages"`
}

func (s *Server) listAgentConversations(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r)
	if !ok {
		return
	}
	convs, err := s.Store.ListAgentConversations(r.Context(), user.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]AgentConversationSummaryDTO, 0, len(convs))
	for _, c := range convs {
		items = append(items, AgentConversationSummaryDTO{ID: c.ID, Title: c.Title, UpdatedAt: c.UpdatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) createAgentConversation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r)
	if !ok {
		return
	}
	conv, err := s.Store.CreateAgentConversation(r.Context(), user.ID, "")
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": conv.ID, "title": conv.Title})
}

func (s *Server) getAgentConversation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r)
	if !ok {
		return
	}
	id, err := conversationID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	conv, err := s.Store.GetAgentConversation(r.Context(), user.ID, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	msgs, err := s.Store.ListAgentMessages(r.Context(), user.ID, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := AgentConversationDTO{ID: conv.ID, Title: conv.Title, Messages: make([]AgentMessageDTO, 0, len(msgs))}
	for _, m := range msgs {
		out.Messages = append(out.Messages, AgentMessageDTO{ID: m.ID, Role: m.Role, Text: m.Text,
			Tools: nonNil(m.Tools), Interrupted: m.Interrupted, CreatedAt: m.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
}

// warnIfTokenOutlivedByRun logs once when run tokens expire before a run at its longest could end.
func (s *Server) warnIfTokenOutlivedByRun(runTimeout time.Duration) {
	if s.Cfg.AgentRunTokenTTL >= runTimeout+time.Minute {
		return
	}
	s.agentReach.ttlWarned.Do(func() {
		s.log().Warn("agent run token lifetime is shorter than the run timeout plus a minute; long runs may lose their tools",
			"run_token_ttl", s.Cfg.AgentRunTokenTTL, "run_timeout", runTimeout)
	})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) deleteAgentConversation(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r)
	if !ok {
		return
	}
	id, err := conversationID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteAgentConversation(r.Context(), user.ID, id); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// chatRequest is the body of POST /api/agent/conversations/{id}/messages.
type chatRequest struct {
	Text string `json:"text"`
	Page struct {
		Path      string `json:"path"`
		ServiceID string `json:"service_id"`
		ClusterID string `json:"cluster_id"`
	} `json:"page"`
}

// pageContext passes the page through only when it is well formed: a path that starts with "/" and holds no
// control characters, and ids that parse as the app's own ids. Anything else is dropped whole, so the model never
// reads text a user or page could have made up.
func pageContext(in chatRequest) agentclient.Page {
	p := in.Page
	if p.Path == "" {
		return agentclient.Page{}
	}
	if !strings.HasPrefix(p.Path, "/") || len(p.Path) > chatPagePathMax || strings.IndexFunc(p.Path, unicode.IsControl) >= 0 {
		return agentclient.Page{}
	}
	if p.ServiceID != "" {
		if _, err := model.ParseServiceID(p.ServiceID); err != nil {
			return agentclient.Page{}
		}
	}
	if p.ClusterID != "" {
		if _, err := model.ParseClusterID(p.ClusterID); err != nil {
			return agentclient.Page{}
		}
	}
	return agentclient.Page{Path: p.Path, ServiceID: p.ServiceID, ClusterID: p.ClusterID}
}

// conversationTitle is the first chatTitleMaxChars characters of the text, on one line.
func conversationTitle(text string) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > chatTitleMaxChars {
		runes = runes[:chatTitleMaxChars]
	}
	return string(runes)
}

// postAgentMessage runs one chat turn: everything up to the reservation answers as a normal JSON error, then the
// answer streams as server-sent events. See chatTurn for how the turn ends.
func (s *Server) postAgentMessage(w http.ResponseWriter, r *http.Request) {
	user, ok := s.chatUser(w, r)
	if !ok {
		return
	}
	convID, err := conversationID(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	if _, err := s.Store.GetAgentConversation(r.Context(), user.ID, convID); err != nil {
		s.fail(w, err)
		return
	}
	var body chatRequest
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	text := strings.TrimSpace(body.Text)
	if n := utf8.RuneCountInString(text); n < 1 || n > chatTextMaxChars {
		s.fail(w, invalid("Write a message of 1 to 8000 characters."))
		return
	}
	if strings.ContainsRune(text, 0) {
		s.fail(w, invalid("The message holds a character that cannot be sent."))
		return
	}

	release, got, err := s.Limits.Acquire(r.Context(), limitAgentChat, user.ID, 1)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer release()
	if !got {
		s.fail(w, errChatBusy)
		return
	}

	settings, err := s.Store.GetAgentSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	ref, chosen := settings.RoleModels[chatRoleName]
	if !chosen {
		s.fail(w, errNotConfigured)
		return
	}
	cat, err := s.catalogue(r)
	if err != nil {
		if r.Context().Err() == nil {
			s.log().Warn("agent catalogue unavailable", "error", err)
		}
		s.fail(w, errAgentDown)
		return
	}
	chosenModel, ok := findModel(cat, ref)
	if !ok {
		s.fail(w, errNotConfigured)
		return
	}

	runTimeout := cat.RunTimeout
	if runTimeout <= 0 {
		runTimeout = defaultRunTimeout
	}
	s.warnIfTokenOutlivedByRun(runTimeout)

	run := store.AgentRun{
		ID: uuid.New(), Role: chatRoleName, UserID: &user.ID, ConversationID: &convID,
		Provider: ref.Provider, Model: ref.Model,
		ReservedMicroUSD: chosenModel.MaxRunCostMicroUSD(cat.MaxRunTokens),
	}
	if err := s.reserveRun(r, run, runTimeout); err != nil {
		s.fail(w, err)
		return
	}

	turn := &chatTurn{
		s: s, r: r, user: user, run: run, text: text, page: pageContext(body), release: release,
		redactEnv: !slices.Contains(settings.UnredactedProviders, ref.Provider),
		timeout:   runTimeout,
	}
	turn.execute(w)
}
