package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/secretshape"
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

// testCallLimit bounds the "Test model" call; the client's own limit is shorter.
var testCallLimit = agentrun.Limit{Normal: 2 * time.Minute, Draining: 2 * time.Second}

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
	// Version is echoed back by PUT; a save made from an older version is refused with 409 settings_changed.
	Version             int64              `json:"version"`
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
		Version:             settings.Version,
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

// agentSettingsUpdate is the PUT body. A null or missing list, map or version decodes to nil, which is refused: a
// save always states the whole form, so a client that forgets a field cannot silently clear it.
type agentSettingsUpdate struct {
	MonthlyBudgetUSD    string                      `json:"monthly_budget_usd"`
	UnredactedProviders []string                    `json:"unredacted_providers"`
	RoleModels          map[string]agentModelChoice `json:"role_models"`
	Version             *int64                      `json:"version"`
}

var errSettingsChanged = &APIError{Status: http.StatusConflict, Code: "settings_changed",
	Message: "These settings were changed by someone else. Reload and try again."}

func modelLabel(ref store.AgentModelRef, ok bool) string {
	if !ok {
		return ""
	}
	return ref.Provider + "/" + ref.Model
}

// putAgentSettings saves the allowance, redaction and chat model. The form must carry the version it was loaded
// at; the save reads the stored settings under the row lock and refuses (409) if they moved on. Everything that
// depends on the stored values (what changed, the audit's old values) uses that locked read.
//
// A role model or provider that is new must be in the agent's catalogue; one already stored may stay as it is
// although the operator no longer offers it, so an admin can still change the allowance, and a removed provider can
// be dropped. While the agent cannot be asked, additions and changes are refused (503); removals still go through.
func (s *Server) putAgentSettings(w http.ResponseWriter, r *http.Request) {
	var body agentSettingsUpdate
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.UnredactedProviders == nil || body.RoleModels == nil || body.Version == nil {
		s.fail(w, invalid("Send the allowance, the redaction list, the role models and the version."))
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

	// The catalogue is fetched before the transaction: the row lock is never held across a network call.
	cat, catErr := s.catalogue(r)
	if catErr != nil && r.Context().Err() == nil {
		s.log().Warn("agent catalogue unavailable", "error", catErr)
	}

	old, err := s.Store.UpdateAgentSettings(r.Context(), *body.Version, func(old store.AgentSettings) (store.AgentSettings, error) {
		oldChat, hadChat := old.RoleModels[chatRoleName]
		chatChanged := (wantChat != nil) != hadChat || (wantChat != nil && *wantChat != oldChat)
		// Only additions and changes need the agent's word; dropping a provider or the chat model is the safe direction.
		unredactedAdded := slices.ContainsFunc(wantUnredacted, func(p string) bool { return !slices.Contains(old.UnredactedProviders, p) })
		if (wantChat != nil && chatChanged) || unredactedAdded {
			if catErr != nil {
				return store.AgentSettings{}, errAgentDown
			}
			if wantChat != nil && chatChanged {
				if _, offered := findModel(cat, *wantChat); !offered {
					return store.AgentSettings{}, invalid("That model is not offered by the agent.")
				}
			}
			for _, p := range wantUnredacted {
				if !slices.Contains(old.UnredactedProviders, p) && !offersProvider(cat, p) {
					return store.AgentSettings{}, invalid("The agent does not offer the provider " + p + ".")
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
		return next, nil
	})
	if err != nil {
		if errors.Is(err, store.ErrAgentSettingsChanged) {
			err = errSettingsChanged
		}
		s.fail(w, err)
		return
	}
	oldChat, hadChat := old.RoleModels[chatRoleName]
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
// settled like any other, so it counts toward the month's allowance, and it ends like any other (finishRun), once
// however the call ends.
func (s *Server) testAgentModel(w http.ResponseWriter, r *http.Request) {
	var body agentModelChoice
	if err := decodeStrictJSON(w, r, &body); err != nil {
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
	if err := s.reserveRun(r, run, cat.RunTimeout); err != nil {
		s.fail(w, err)
		return
	}

	meter := &agentrun.Meter{Store: s.Store, RunID: run.ID, Now: s.now, Draining: s.draining}
	var once sync.Once
	finish := func(ok bool) {
		once.Do(func() {
			res := turnResult{outcome: "error"}
			if ok {
				res = turnResult{success: true, outcome: "done"}
			}
			s.finishRun(r, s.runOps(r, run, actor, meter, res))
		})
	}
	defer finish(false) // a panic still settles the run and deletes its token

	token, err := agentrun.Within(r.Context(), setUpLimit, s.draining, func(ctx context.Context) (string, error) {
		return s.Auth.MintRunToken(ctx, actor, run.ID, true)
	})
	if err != nil {
		finish(false)
		s.log().Error("agent test run token not minted", "run_id", run.ID, "error", err)
		s.fail(w, err)
		return
	}
	select {
	case <-s.draining: // no paid call is started once the server shuts down
		finish(false)
		s.fail(w, errAgentDown)
		return
	default:
	}
	// Detached from the request: the model is paid for its answer whether or not the admin waits for it.
	result, err := agentrun.Within(context.WithoutCancel(r.Context()), testCallLimit, s.draining, func(ctx context.Context) (agentclient.TestResult, error) {
		return s.Agent.Test(ctx, token, body.Provider, body.Model)
	})
	switch {
	case err == nil && result.UsageKnown:
		meter.See(agentrun.Usage{InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, CostMicroUSD: result.CostMicroUSD})
	case !errors.Is(err, agentclient.ErrNoPaidCall):
		// What the call cost is unknown, so it counts as all that was set aside for it.
		meter.See(agentrun.Usage{CostMicroUSD: run.ReservedMicroUSD})
	}
	if err != nil {
		finish(false)
		s.logRunError(run.ID, "agent test call failed", err)
		s.fail(w, errAgentDown)
		return
	}
	if result.CostMicroUSD > run.ReservedMicroUSD {
		// Never capped: the budget must know the real spend.
		s.log().Warn("agent test cost above its reservation", "run_id", run.ID,
			"cost_micro_usd", result.CostMicroUSD, "reserved_micro_usd", run.ReservedMicroUSD)
	}
	finish(result.OK)
	writeJSON(w, http.StatusOK, cleanTestResult(result))
}

// testErrorKinds are the failure kinds the agent documents for a test call.
var testErrorKinds = []string{"auth", "rate_limited", "unavailable", "invalid_request"}

// cleanTestResult is what the browser is told of a test call: a known kind, and a message scrubbed of anything
// secret-shaped and cut to the length of any other agent message. A failure with an unknown kind is "unavailable".
func cleanTestResult(res agentclient.TestResult) AgentTestResultDTO {
	message := secretshape.ScrubText(res.Message)
	if res.OK {
		return AgentTestResultDTO{OK: true, Kind: "ok", Message: boundedMessage(message)}
	}
	kind := res.Kind
	if !slices.Contains(testErrorKinds, kind) {
		kind = "unavailable"
	}
	return AgentTestResultDTO{OK: false, Kind: kind, Message: errorMessageFrom(message)}
}

func boundedMessage(msg string) string {
	if runes := []rune(msg); len(runes) > maxErrorMessageLen {
		return string(runes[:maxErrorMessageLen])
	}
	return msg
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
	if err := decodeStrictJSON(w, r, &body); err != nil {
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
	was, err := s.Store.ChangeUserAIChat(r.Context(), id, *body.On)
	if err != nil {
		s.fail(w, err)
		return
	}
	if was != *body.On {
		summary := "Turned AI chat on for " + target.Username + "."
		if !*body.On {
			summary = "Turned AI chat off for " + target.Username + "."
		}
		s.auditAdmin(r, "user_ai_chat", "user:"+target.Username, summary, map[string]any{"user_id": target.ID, "on": *body.On})
	}
	w.WriteHeader(http.StatusNoContent)
}
