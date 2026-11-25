// Package agentjob drives one paid background run of the agent: reserve, mint a run token, call the agent, meter its
// usage, settle, delete the token and audit, each exactly once. The jobs that run in the background (the investigator,
// the reviewer) call it and keep what is theirs: their own claim, the task, and what becomes of the answer. It imports
// neither of them nor the web layer.
package agentjob

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/agentresult"
	"github.com/rcss-labs/harbormaster/internal/agentrun"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Kind is how a driver step or a run came out. Any value that is not listed is a Failed one.
type Kind int

const (
	Failed           Kind = iota // the zero value, so nothing unforeseen looks like success
	Ready                        // Prepare: a run may be started
	Done                         // the agent finished and sent a result
	NoResult                     // the agent finished without one and said so
	LimitReached                 // the agent finished without a result, having hit a limit
	Budget                       // the reservation was refused: nothing was spent
	NotConfigured                // no model for the role, or the agent does not offer it
	AgentBusy                    // the agent refused the run before it did anything
	AgentUnavailable             // the agent could not be reached, or its stream broke
	Cancelled                    // the context ended, or the lease was not certain
	Interrupted                  // the run was settled elsewhere, or its usage could not be recorded
	Conflict                     // another holder has the caller's claim
	// Failed is also the kind of a set-up step that failed, a run that errored or ran too long, and a panic.
)

// DriverStore is what the driver reads and writes of the store itself; the run's own rows go through Runs.
type DriverStore interface {
	agentrun.UsageStore
	EnsureAgentIdentity(ctx context.Context) (store.User, error)
	LoadSubject(ctx context.Context, userID int64) (authz.Subject, error)
	GetAgentSettings(ctx context.Context) (store.AgentSettings, error)
	Audit(ctx context.Context, e store.AuditEntry) error
}

// Minter makes the run token the agent calls back with.
type Minter interface {
	MintAgentRunToken(ctx context.Context, runID uuid.UUID, redactEnv bool) (raw string, identity store.User, err error)
}

// RunClient is the agent service.
type RunClient interface {
	Catalogue(ctx context.Context) (agentclient.Catalogue, error)
	Run(ctx context.Context, bearer string, req agentclient.RunRequest, onEvent func(agentclient.Event)) error
}

// Driver runs background runs for jobs. It is cheap: a job may make one for each run.
type Driver struct {
	Store DriverStore
	Auth  Minter
	Agent RunClient
	Runs  *agentrun.Runner
	// Guard is checked before and after the reservation, so a coordinator that lost its lease starts nothing. Nil
	// means always allowed.
	Guard lease.Guard
	Now   func() time.Time
	Log   *slog.Logger

	// Set by tests only.
	OnEvent func(agentclient.Event)
	Slack   time.Duration // past the agent's run timeout; 0 means DefaultSlack
}

// DefaultSlack is how long past the agent's own run timeout a run is waited for.
const DefaultSlack = 30 * time.Second

// Words of the audit log for the endings the driver itself words.
const (
	wordInterrupted = "interrupted"
)

// Prepared is what a run needs, found without spending anything.
type Prepared struct {
	Identity  store.User
	Subject   authz.Subject
	Catalogue agentclient.Catalogue
	Model     agentclient.Model
	Provider  string
	RedactEnv bool
}

// Job is one run to make. Everything the driver does not know of the caller is in here.
type Job struct {
	Role    string
	Task    agentclient.Task
	Details map[string]any // added to the run's audit row; ids and fixed words only
	// Started records the run under the caller's claim, after it is reserved. store.ErrConflict ends the run.
	Started func(ctx context.Context, runID uuid.UUID, provider, model string) error
	// Renew, when set, runs after the run token is made, once the run's timeout is known.
	Renew func(ctx context.Context, runTimeout time.Duration) error
	// SetUpOver, when set, is called once: just before the agent is called, or when set-up stopped without calling it,
	// and in both cases before the run is settled. A caller that does its set-ups one at a time lets the next one in.
	SetUpOver func()
	// Conclude, when set, is called once for a run that was reserved, with how it came out, on its way to being
	// settled. Nil means the default words and nothing more to save.
	Conclude func(Outcome) Conclusion
}

// Conclusion is what the caller makes of an outcome.
type Conclusion struct {
	// Word is the run's outcome in the audit log, a fixed one.
	Word string
	// Save writes what becomes of the caller's own record. It runs together with the settling of the run, bounded and
	// on a context that the end of the job's does not cut off; the audit row says whether it succeeded. Nil: none.
	Save func(ctx context.Context) error
}

// Outcome is how a run came out. The result is what the agent sent, not accepted: the caller does that.
type Outcome struct {
	Kind     Kind
	RunID    uuid.UUID
	Provider string
	Model    string
	Tools    []string
	Result   *agentresult.Result // of a Done run only

	// Called: the agent was called. A Cancelled one that was not called was not started at all.
	Called bool
	// Panicked: the run was ended by a panic.
	Panicked bool
	// ModelUnreachable: the agent reported that it could not reach its model.
	ModelUnreachable bool
	// Finished: the run was reserved and has been settled, its token deleted and its audit row written. Conclude
	// ran. SaveErr is why Save failed, if it did. When not Finished, nothing was reserved or the reservation was
	// refused, and the caller ends its own record.
	Finished bool
	SaveErr  error
}

// Prepare spends nothing: it finds the agent's identity, what it may see, the model chosen for the role and the
// agent's catalogue. Kinds: Ready, NotConfigured (no model for the role, the model is not offered, the role is not
// offered, or no run could be priced), AgentUnavailable (no catalogue), Failed (a read failed).
func (d *Driver) Prepare(ctx context.Context, role string) (Prepared, Kind) {
	identity, kind := d.Identify(ctx)
	if kind != Ready {
		return Prepared{}, kind
	}
	subject, err := Within(ctx, func(ctx context.Context) (authz.Subject, error) { return d.Store.LoadSubject(ctx, identity.ID) })
	if err != nil {
		d.logError(ctx, "agentjob: load what the agent identity may see", err)
		return Prepared{}, Failed
	}
	subject.ReadOnly = true
	p, kind := d.PrepareWith(ctx, identity, role)
	p.Subject = subject
	return p, kind
}

// Identify loads the agent's identity. Kinds: Ready, Failed.
func (d *Driver) Identify(ctx context.Context) (store.User, Kind) {
	identity, err := Within(ctx, d.Store.EnsureAgentIdentity)
	if err != nil {
		d.logError(ctx, "agentjob: load the agent identity", err)
		return store.User{}, Failed
	}
	return identity, Ready
}

// PrepareWith is Prepare for a caller that has the identity already and has things to decide between the two; the
// Subject is left empty.
func (d *Driver) PrepareWith(ctx context.Context, identity store.User, role string) (Prepared, Kind) {
	settings, err := Within(ctx, d.Store.GetAgentSettings)
	if err != nil {
		d.logError(ctx, "agentjob: load the agent settings", err)
		return Prepared{}, Failed
	}
	ref, chosen := settings.RoleModels[role]
	if !chosen {
		return Prepared{}, NotConfigured
	}
	cat, err := d.Agent.Catalogue(ctx)
	if err != nil {
		d.logError(ctx, "agentjob: agent catalogue unavailable", err)
		return Prepared{}, AgentUnavailable
	}
	chosenModel, offered := cat.Find(ref.Provider, ref.Model)
	_, known := worstCase(chosenModel, cat.MaxRunTokens)
	if !offered || !cat.Supports(role) || !known {
		return Prepared{}, NotConfigured
	}
	return Prepared{
		Identity: identity, Catalogue: cat, Model: chosenModel, Provider: ref.Provider,
		RedactEnv: !slices.Contains(settings.UnredactedProviders, ref.Provider),
	}, Ready
}

// worstCase is the most a run can cost. It is not known, and such a run must not start, when the agent states no
// price or no limit, or a product of the two that no sum can hold.
func worstCase(m agentclient.Model, maxRunTokens int64) (microUSD int64, known bool) {
	price := max(m.InputMicroUSDPerMTok, m.OutputMicroUSDPerMTok)
	if maxRunTokens <= 0 || price <= 0 || price > math.MaxInt64/maxRunTokens {
		return 0, false
	}
	microUSD = m.MaxRunCostMicroUSD(maxRunTokens)
	return microUSD, microUSD > 0
}

// EndEarlier ends the run of an earlier holder that did not live to do it: it keeps the figures last recorded. One
// that was ended already stays as it is, and one that is gone is not an error. An error means that it may still be
// running: no second run beside it.
func (d *Driver) EndEarlier(ctx context.Context, identity store.User, run store.AgentRun, details map[string]any) error {
	err := d.Runs.End(ctx, run, agentrun.Actor{Name: identity.Username}, "", agentrun.Ending{Outcome: wordInterrupted}, details).SettleErr
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	return err
}

// AuditSkippedBudget records a run that the budget refused, which out names. It has no run row, so it is an ordinary
// audit entry.
func (d *Driver) AuditSkippedBudget(ctx context.Context, p Prepared, out Outcome, role string, details map[string]any) {
	run := store.AgentRun{ID: out.RunID, Role: role, UserID: &p.Identity.ID, Provider: out.Provider, Model: out.Model}
	entry := store.AgentRunAudit(run, p.Identity.Username, "skipped_budget")
	for k, v := range details {
		entry.Details[k] = v
	}
	if err := Step(ctx, func(ctx context.Context) error { return d.Store.Audit(ctx, entry) }); err != nil {
		d.logRunError(run.ID, "agentjob: could not audit the refused run", err)
	}
}

func (d *Driver) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Driver) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// logError logs err, of a panic only its type and stack. Nothing is logged once ctx has ended: the step stopped for
// that reason.
func (d *Driver) logError(ctx context.Context, what string, err error) {
	if ctx.Err() == nil {
		d.logErr(what, err)
	}
}

func (d *Driver) logErr(what string, err error) {
	var p *agentrun.PanicError
	if errors.As(err, &p) {
		d.log().Error(what, "panic_type", p.Type, "stack", p.Stack)
		return
	}
	d.log().Warn(what, "err", err)
}

func (d *Driver) logRunError(runID uuid.UUID, what string, err error) {
	d.Runs.LogRunError(runID, what, err)
}

// Confirm reports why nothing may be started: the job is being stopped, or the lease is not certain.
func Confirm(ctx context.Context, guard lease.Guard) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard == nil {
		return nil
	}
	return guard(ctx)
}

// Within makes a store step that is not cut off by the end of ctx, which would leave unknown whether it was made: it
// is bounded by its own limit, a short one once ctx has ended.
func Within[T any](ctx context.Context, fn func(ctx context.Context) (T, error)) (T, error) {
	return agentrun.Within(context.WithoutCancel(ctx), agentrun.SetUpLimit, ctx.Done(), fn)
}

// Step is Within for a step that returns nothing.
func Step(ctx context.Context, fn func(ctx context.Context) error) error {
	_, err := Within(ctx, func(ctx context.Context) (struct{}, error) { return struct{}{}, fn(ctx) })
	return err
}
