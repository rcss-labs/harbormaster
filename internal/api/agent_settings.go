package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	microUSDPerUSD     = 1_000_000
	microUSDPerCent    = 10_000
	maxBudgetUSDDigits = 7 // "1000000" is the largest whole-dollar amount
	maxBudgetMicroUSD  = 1_000_000 * microUSDPerUSD
	testRoleName       = "test"
	testRunTokens      = 2000 // the "Test model" call reserves a run of this many tokens at the model's higher price
)

// parseUSD reads a dollar amount such as "25", "25.5" or "25.50" into micro-dollars. Only digits and at most two
// decimal places are accepted (no sign, exponent, spaces or separators), and the amount must not exceed 1,000,000.
// It never touches a float, so every accepted amount is exact.
func parseUSD(in string) (int64, bool) {
	whole, frac, hasPoint := strings.Cut(in, ".")
	if len(whole) < 1 || len(whole) > maxBudgetUSDDigits || !allASCIIDigits(whole) {
		return 0, false
	}
	var cents int64
	if hasPoint {
		if len(frac) < 1 || len(frac) > 2 || !allASCIIDigits(frac) {
			return 0, false
		}
		for _, d := range frac {
			cents = cents*10 + int64(d-'0')
		}
		if len(frac) == 1 {
			cents *= 10
		}
	}
	var dollars int64
	for _, d := range whole {
		dollars = dollars*10 + int64(d-'0')
	}
	micro := dollars*microUSDPerUSD + cents*microUSDPerCent
	if micro > maxBudgetMicroUSD {
		return 0, false
	}
	return micro, true
}

func allASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// formatUSD writes micro-dollars as dollars with two places. Money the app has spent or holds rounds up to the
// next cent (roundUp), so the page never shows less than was used; an allowance, which is always a whole number
// of cents when it was set here, is shown as stored. Negative amounts show as zero.
func formatUSD(micro int64, roundUp bool) string {
	micro = max(micro, 0)
	cents := micro / microUSDPerCent
	if roundUp && micro%microUSDPerCent != 0 {
		cents++
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// AgentCatalogueModelDTO is one model the agent offers, with its prices in micro-dollars per million tokens.
type AgentCatalogueModelDTO struct {
	ID                    string `json:"id"`
	InputPerMTokMicroUSD  int64  `json:"input_per_mtok_micro_usd"`
	OutputPerMTokMicroUSD int64  `json:"output_per_mtok_micro_usd"`
	ContextTokens         int64  `json:"context_tokens"`
}

type AgentCatalogueProviderDTO struct {
	Provider string                   `json:"provider"`
	Models   []AgentCatalogueModelDTO `json:"models"`
}

// AgentChatModelDTO is the model chosen for chat. Available is false when the agent is reachable and no longer
// offers it; while the agent cannot be asked it stays true, so a model is not called gone for want of an answer.
type AgentChatModelDTO struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Available bool   `json:"available"`
}

type AgentRoleModelsDTO struct {
	Chat *AgentChatModelDTO `json:"chat,omitempty"`
}

type AgentSpendDTO struct {
	Month       string `json:"month"`
	SettledUSD  string `json:"settled_usd"`
	ReservedUSD string `json:"reserved_usd"`
}

// AgentSettingsDTO is the body of GET and PUT /api/agent/settings.
type AgentSettingsDTO struct {
	MonthlyBudgetUSD    string             `json:"monthly_budget_usd"`
	UnredactedProviders []string           `json:"unredacted_providers"`
	RoleModels          AgentRoleModelsDTO `json:"role_models"`
	Catalogue           struct {
		Providers []AgentCatalogueProviderDTO `json:"providers"`
	} `json:"catalogue"`
	AgentReachable bool          `json:"agent_reachable"`
	Spend          AgentSpendDTO `json:"spend"`
}

func (s *Server) agentSettingsDTO(r *http.Request) (AgentSettingsDTO, error) {
	settings, err := s.Store.GetAgentSettings(r.Context())
	if err != nil {
		return AgentSettingsDTO{}, err
	}
	settled, reserved, err := s.Store.AgentSpend(r.Context(), s.now())
	if err != nil {
		return AgentSettingsDTO{}, err
	}
	out := AgentSettingsDTO{
		MonthlyBudgetUSD:    formatUSD(settings.MonthlyBudgetMicroUSD, false),
		UnredactedProviders: append([]string{}, settings.UnredactedProviders...),
		Spend: AgentSpendDTO{Month: s.now().UTC().Format("2006-01"),
			SettledUSD: formatUSD(settled, true), ReservedUSD: formatUSD(reserved, true)},
	}
	out.Catalogue.Providers = []AgentCatalogueProviderDTO{}
	cat, catErr := s.catalogue(r)
	out.AgentReachable = catErr == nil
	for _, p := range cat.Providers {
		dto := AgentCatalogueProviderDTO{Provider: p.Provider, Models: []AgentCatalogueModelDTO{}}
		for _, m := range p.Models {
			dto.Models = append(dto.Models, AgentCatalogueModelDTO{ID: m.ID, InputPerMTokMicroUSD: m.InputMicroUSDPerMTok,
				OutputPerMTokMicroUSD: m.OutputMicroUSDPerMTok, ContextTokens: m.ContextTokens})
		}
		out.Catalogue.Providers = append(out.Catalogue.Providers, dto)
	}
	if ref, ok := settings.RoleModels[chatRoleName]; ok {
		_, offered := findModel(cat, ref)
		out.RoleModels.Chat = &AgentChatModelDTO{Provider: ref.Provider, Model: ref.Model, Available: offered || catErr != nil}
	}
	return out, nil
}

func (s *Server) getAgentSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.agentSettingsDTO(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type agentModelChoice struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type agentSettingsUpdate struct {
	MonthlyBudgetUSD    string                      `json:"monthly_budget_usd"`
	UnredactedProviders []string                    `json:"unredacted_providers"`
	RoleModels          map[string]agentModelChoice `json:"role_models"`
}

// sameSet reports whether a and b hold the same entries, in any order.
func sameSet(a, b []string) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(x string) bool { return !slices.Contains(b, x) })
}

func modelLabel(ref store.AgentModelRef, ok bool) string {
	if !ok {
		return ""
	}
	return ref.Provider + "/" + ref.Model
}

// putAgentSettings saves the allowance, redaction and chat model. A role model or provider that is new must be in
// the agent's catalogue; one already stored may stay as it is although the operator no longer offers it, so an
// admin can still change the allowance, and a removed provider can be dropped. While the agent cannot be asked,
// only a request that leaves the models and redaction unchanged is accepted.
func (s *Server) putAgentSettings(w http.ResponseWriter, r *http.Request) {
	var body agentSettingsUpdate
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	budget, ok := parseUSD(body.MonthlyBudgetUSD)
	if !ok {
		s.fail(w, invalid("Enter an amount in dollars from 0 to 1000000, with at most two decimal places."))
		return
	}
	var wantChat *store.AgentModelRef
	for role, choice := range body.RoleModels {
		if role != chatRoleName {
			s.fail(w, invalid("Unknown role: "+role+"."))
			return
		}
		if choice.Provider == "" || choice.Model == "" {
			s.fail(w, invalid("Choose a provider and a model."))
			return
		}
		wantChat = &store.AgentModelRef{Provider: choice.Provider, Model: choice.Model}
	}
	var wantUnredacted []string
	for _, p := range body.UnredactedProviders {
		if !slices.Contains(wantUnredacted, p) {
			wantUnredacted = append(wantUnredacted, p)
		}
	}

	old, err := s.Store.GetAgentSettings(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	oldChat, hadChat := old.RoleModels[chatRoleName]
	chatChanged := (wantChat != nil) != hadChat || (wantChat != nil && *wantChat != oldChat)
	unredactedChanged := !sameSet(wantUnredacted, old.UnredactedProviders)
	if chatChanged || unredactedChanged {
		cat, err := s.catalogue(r)
		if err != nil {
			if r.Context().Err() == nil {
				s.log().Warn("agent catalogue unavailable", "error", err)
			}
			s.fail(w, errAgentDown)
			return
		}
		if chatChanged && wantChat != nil {
			if _, offered := findModel(cat, *wantChat); !offered {
				s.fail(w, invalid("That model is not offered by the agent."))
				return
			}
		}
		for _, p := range wantUnredacted {
			if !slices.Contains(old.UnredactedProviders, p) && !offersProvider(cat, p) {
				s.fail(w, invalid("The agent does not offer the provider "+p+"."))
				return
			}
		}
	}

	next := store.AgentSettings{MonthlyBudgetMicroUSD: budget, UnredactedProviders: wantUnredacted, RoleModels: map[string]store.AgentModelRef{}}
	for role, ref := range old.RoleModels {
		next.RoleModels[role] = ref
	}
	delete(next.RoleModels, chatRoleName)
	if wantChat != nil {
		next.RoleModels[chatRoleName] = *wantChat
	}
	if err := s.Store.PutAgentSettings(r.Context(), next); err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "agent_settings_update", "agent:settings", "Changed the AI agent settings.", map[string]any{
		"old_budget_usd":           formatUSD(old.MonthlyBudgetMicroUSD, false),
		"new_budget_usd":           formatUSD(budget, false),
		"old_chat_model":           modelLabel(oldChat, hadChat),
		"new_chat_model":           modelLabelPtr(wantChat),
		"old_unredacted_providers": nonNil(old.UnredactedProviders),
		"new_unredacted_providers": nonNil(wantUnredacted),
	})
	out, err := s.agentSettingsDTO(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func modelLabelPtr(ref *store.AgentModelRef) string {
	if ref == nil {
		return ""
	}
	return modelLabel(*ref, true)
}

func offersProvider(cat agentclient.Catalogue, provider string) bool {
	return slices.ContainsFunc(cat.Providers, func(p agentclient.ProviderModels) bool { return p.Provider == provider })
}

// AgentTestResultDTO is the body of POST /api/agent/settings/test.
type AgentTestResultDTO struct {
	OK      bool   `json:"ok"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// testAgentModel makes one small paid call to the chosen model. It is a run with role "test", reserved and
// settled like any other, so it counts toward the month's allowance. The run ends in one place (finishTestRun),
// which settles it, deletes its token and runs once however the call ends.
func (s *Server) testAgentModel(w http.ResponseWriter, r *http.Request) {
	var body agentModelChoice
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
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
	chosen, offered := findModel(cat, store.AgentModelRef{Provider: body.Provider, Model: body.Model})
	if !offered {
		s.fail(w, invalid("That model is not offered by the agent."))
		return
	}
	actor, _ := auth.UserFrom(r.Context())
	run := store.AgentRun{
		ID: uuid.New(), Role: testRoleName, UserID: &actor.ID, Provider: body.Provider, Model: body.Model,
		ReservedMicroUSD: chosen.MaxRunCostMicroUSD(testRunTokens),
	}
	if err := s.Store.ReserveAgentRun(r.Context(), run, max(2*cat.RunTimeout, minRunStaleAfter), s.now()); err != nil {
		if errors.Is(err, store.ErrBudgetExhausted) {
			err = errBudget
		}
		s.fail(w, err)
		return
	}

	var (
		once   sync.Once
		state  = "failed"
		result agentclient.TestResult
	)
	finish := func() {
		once.Do(func() { s.finishTestRun(r, run.ID, state, result) })
	}
	defer finish() // a panic still settles the run and deletes its token

	token, err := s.Auth.MintRunToken(r.Context(), actor, run.ID, true)
	if err == nil {
		result, err = s.Agent.Test(r.Context(), token, body.Provider, body.Model)
	}
	if err != nil {
		finish()
		if r.Context().Err() == nil {
			s.log().Warn("agent test call failed", "run_id", run.ID, "error", err)
		}
		s.fail(w, errAgentDown)
		return
	}
	if result.OK {
		state = "done"
	}
	finish()
	writeJSON(w, http.StatusOK, AgentTestResultDTO{OK: result.OK, Kind: result.Kind, Message: result.Message})
}

// finishTestRun records what the call cost, settles the run and deletes its token, each step whether or not the
// one before it failed; they are the same steps a chat turn ends with.
func (s *Server) finishTestRun(r *http.Request, runID uuid.UUID, state string, result agentclient.TestResult) {
	_ = s.runStep(r, runID, "record the test cost", func(ctx context.Context) error {
		if result.CostMicroUSD <= 0 && result.InputTokens <= 0 && result.OutputTokens <= 0 {
			return nil
		}
		err := s.Store.RecordAgentRunUsage(ctx, runID, max(result.InputTokens, 0), max(result.OutputTokens, 0),
			max(result.CostMicroUSD, 0), s.now())
		if errors.Is(err, store.ErrAgentRunNotRunning) {
			return nil
		}
		return err
	})
	_ = s.runStep(r, runID, "settle the run", func(ctx context.Context) error { return s.settleRun(ctx, runID, state, nil) })
	_ = s.runStep(r, runID, "delete the run token", func(ctx context.Context) error { return s.dropRunToken(ctx, runID) })
}

// AgentUsageDayDTO is one day and role of finished usage.
type AgentUsageDayDTO struct {
	Day          string `json:"day"`
	Role         string `json:"role"`
	Runs         int64  `json:"runs"`
	CostUSD      string `json:"cost_usd"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
}

// AgentUsageDTO is the body of GET /api/agent/usage.
type AgentUsageDTO struct {
	Month string             `json:"month"`
	Days  []AgentUsageDayDTO `json:"days"`
}

func (s *Server) getAgentUsage(w http.ResponseWriter, r *http.Request) {
	days, err := s.Store.AgentUsageByDay(r.Context(), s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	out := AgentUsageDTO{Month: s.now().UTC().Format("2006-01"), Days: make([]AgentUsageDayDTO, 0, len(days))}
	for _, d := range days {
		out.Days = append(out.Days, AgentUsageDayDTO{Day: d.Day.Format("2006-01-02"), Role: d.Role, Runs: d.Runs,
			CostUSD: formatUSD(d.CostMicroUSD, true), InputTokens: d.InputTokens, OutputTokens: d.OutputTokens})
	}
	writeJSON(w, http.StatusOK, out)
}

// setUserAIChat grants or revokes a user's access to the agent chat. A change either way is audited.
func (s *Server) setUserAIChat(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.On == nil {
		s.fail(w, invalid("Say whether chat is on or off."))
		return
	}
	target, err := s.Store.GetUser(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.SetUserAIChat(r.Context(), id, *body.On); err != nil {
		s.fail(w, err)
		return
	}
	if target.AIChat != *body.On {
		summary := "Turned AI chat on for " + target.Username + "."
		if !*body.On {
			summary = "Turned AI chat off for " + target.Username + "."
		}
		s.auditAdmin(r, "user_ai_chat", "user:"+target.Username, summary, map[string]any{"user_id": target.ID, "on": *body.On})
	}
	w.WriteHeader(http.StatusNoContent)
}
