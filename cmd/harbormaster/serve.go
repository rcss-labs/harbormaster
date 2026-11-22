package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/rcss-labs/harbormaster/internal/api"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/coordinator"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/lease"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/notify/fakesink"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/platforms"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/registry"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/safedial"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// grypeLatestURL is Anchore's listing of the current Grype vulnerability database.
const grypeLatestURL = "https://grype.anchore.io/databases/v6/latest.json"

const serveUsage = "usage: harbormaster serve [--fake-aws] [--role=all|web|poller|coordinator]"

const (
	// leaseLoopStaleAfter is how long a poller's lease manager may go without finishing a tick before /healthz says 503.
	leaseLoopStaleAfter = 60 * time.Second
	shutdownTimeout     = 15 * time.Second
	// fakeRolloutStep is how often the fake AWS estate advances rollouts.
	fakeRolloutStep = 2400 * time.Millisecond
)

// errUsage marks command-line mistakes so main can exit with status 2.
type errUsage struct{ msg string }

func (e errUsage) Error() string { return e.msg }

// storeRoles resolves the role to assume for an account from the aws_accounts table.
type storeRoles struct{ st *store.Store }

func (r storeRoles) Role(ctx context.Context, accountID string) (string, string, error) {
	account, err := r.st.GetAccountByAccountID(ctx, accountID)
	if errors.Is(err, store.ErrNotFound) {
		return "", "", fmt.Errorf("account %s: %w", accountID, awsx.ErrAccountUnknown)
	}
	if err != nil {
		return "", "", err
	}
	if !account.Enabled {
		return "", "", fmt.Errorf("account %s is disabled: %w", accountID, awsx.ErrAccountUnknown)
	}
	return account.RoleARN, account.ExternalID, nil
}

// databaseURLFromSecret accepts either a plain postgres URL or the RDS secret JSON
// {"username","password","host","port","dbname"[,"sslmode"]} and returns a postgres URL.
// Errors never include the secret's content.
func databaseURLFromSecret(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "{") {
		if !strings.HasPrefix(raw, "postgres://") && !strings.HasPrefix(raw, "postgresql://") {
			return "", errors.New("database secret is not a postgres URL or JSON object")
		}
		return raw, nil
	}
	var s struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Host     string `json:"host"`
		Port     any    `json:"port"`
		DBName   string `json:"dbname"`
		SSLMode  string `json:"sslmode"`
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return "", errors.New("database secret is not valid JSON")
	}
	var missing []string
	for name, value := range map[string]string{"host": s.Host, "username": s.Username, "password": s.Password} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", fmt.Errorf("database secret JSON is missing %s", strings.Join(missing, ", "))
	}
	port := "5432"
	switch p := s.Port.(type) {
	case float64:
		port = strconv.Itoa(int(p))
	case string:
		if p != "" {
			port = p
		}
	}
	dbName := s.DBName
	if dbName == "" {
		dbName = "postgres"
	}
	sslMode := s.SSLMode
	if sslMode == "" {
		sslMode = "require"
	}
	u := url.URL{
		Scheme: "postgres", User: url.UserPassword(s.Username, s.Password),
		Host: net.JoinHostPort(s.Host, port), Path: "/" + dbName,
		RawQuery: url.Values{"sslmode": {sslMode}}.Encode(),
	}
	return u.String(), nil
}

// openStore opens the database, scrubbing the connection string from any error so a
// credential embedded in it never reaches logs.
func openStore(ctx context.Context, dbURL string) (*store.Store, error) {
	return openStoreSized(ctx, dbURL, 0)
}

// openStoreSized is openStore with a pool cap; 0 keeps pgx's default.
func openStoreSized(ctx context.Context, dbURL string, maxConns int) (*store.Store, error) {
	st, err := store.OpenWithOptions(ctx, dbURL, store.OpenOptions{MaxConns: maxConns})
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), dbURL, "[database url]")
		if parsed, perr := url.Parse(dbURL); perr == nil && parsed.User != nil {
			if pw, ok := parsed.User.Password(); ok && pw != "" {
				msg = strings.ReplaceAll(msg, pw, "[redacted]")
			}
		}
		return nil, errors.New(msg)
	}
	return st, nil
}

// resolveDatabaseURL returns HM_DATABASE_URL, or reads the database secret when only its ARN is set.
func resolveDatabaseURL(ctx context.Context, cfg config.Config) (string, error) {
	if cfg.DatabaseURL != "" {
		return cfg.DatabaseURL, nil
	}
	base, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("load aws config: %w", err)
	}
	secret, err := awsx.NewSecretReader(base).ReadSecret(ctx, cfg.DatabaseSecretARN)
	if err != nil {
		return "", fmt.Errorf("read database secret: %w", err)
	}
	return databaseURLFromSecret(secret)
}

func parseLevel(name string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return slog.LevelInfo
	}
	return level
}

// runServe runs the server until ctx is cancelled or SIGINT/SIGTERM-style cancellation arrives.
func runServe(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	fakeFlag := flags.Bool("fake-aws", false, "serve an in-memory AWS estate with demo data")
	roleFlag := flags.String("role", "", "run only this role's parts: all, web, poller or coordinator (default all, or HM_ROLE)")
	if err := flags.Parse(args); err != nil {
		return errUsage{serveUsage}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	if *roleFlag != "" {
		parsed, err := config.ParseRole(*roleFlag)
		if err != nil {
			return errUsage{"--role: must be all, web, poller or coordinator\n" + serveUsage}
		}
		cfg.Role = parsed
	}
	role := cfg.Role
	fakeAWS := *fakeFlag || cfg.FakeAWS
	// The API's SSRF filter relaxes http loopback URLs only when cfg.FakeAWS is set, so the --fake-aws
	// flag must set it too or the demo IdP's metadata cannot be imported.
	cfg.FakeAWS = fakeAWS
	// Fake mode always has image scanning on, backed by the fake scanner, so the demo and the e2e suite need no variables.
	if fakeAWS {
		cfg.Scan.Enabled = true
	}
	// Only a task that coordinates launches scans. A web task needs HM_SCAN_ENABLED to show the feature but not the
	// launch settings, and a poller needs neither.
	if role.Coordinates() {
		if err := cfg.ValidateScanLaunch(); err != nil {
			return err
		}
	}
	log := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	kvOpts, kvConfigured, err := kvOptions(cfg.KV)
	if err != nil {
		return err
	}

	var base aws.Config
	if !fakeAWS {
		if base, err = awsconfig.LoadDefaultConfig(ctx); err != nil {
			return fmt.Errorf("load aws config: %w", err)
		}
	}
	dbURL, err := resolveDatabaseURL(ctx, cfg)
	if err != nil {
		return err
	}

	st, err := openStoreSized(ctx, dbURL, cfg.PoolSize())
	if err != nil {
		return err
	}
	defer st.Close()
	telemetry.Default.SetRoleInfo(string(role), version)
	if applied, err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	} else if len(applied) > 0 {
		log.Info("applied migrations", "versions", applied)
	}

	// The shared store is optional; without it every limit stays per task.
	var kvHealth *kv.Health
	if kvConfigured {
		health, stopKV, err := startKV(ctx, cfg.KV, kvOpts, log)
		if err != nil {
			return err
		}
		defer stopKV()
		kvHealth = health
	}

	// Route secrets are sealed under the session key; without one only secretless routes (email) can exist.
	var notifyBox *secretbox.Box
	if cfg.SessionKey != "" {
		if notifyBox, err = secretbox.New(cfg.SessionKey, secretbox.PurposeNotifySecret); err != nil {
			return fmt.Errorf("notification secret box: %w", err)
		}
	}
	var notifyDemo []devseed.Option
	if cfg.Notify.FakeDemo {
		notifyDemo = []devseed.Option{devseed.WithNotifyDemo(notifyBox, cfg.BaseURL)}
	}

	var (
		clients awsx.Clients
		prober  awsx.Prober
		// plan 04: terminal
		execInsecureStream bool
		// plan 07: the fake IdP exists only in fake AWS mode; nil leaves /fake-idp/ unrouted.
		fakeIdP http.Handler
		// the in-process notification receiver exists only in fake AWS mode; nil leaves /fake-notify/ unrouted.
		fakeNotify http.Handler
		// scanRunner is the image scan loop; nil when scanning is off.
		scanRunner *scanjob.Runner
	)
	if fakeAWS {
		estate := fake.MockupEstate(time.Now)
		f := fake.New(estate)
		// The fake bypasses the SDK middleware, so it feeds the same AWS call counter itself.
		f.OnCall = func(ctx context.Context, t awsx.Target, service, op, result string) {
			telemetry.Default.CountAWSCall(t.AccountID, t.Region, service, op, result, string(awsx.SourceFrom(ctx)))
		}
		// The fake ECS hands out ws:// streams served by an in-process fake SSM agent.
		ssmServer := fake.NewSSMServer(fake.SSMServerOptions{})
		ssmServer.Start()
		defer ssmServer.Close()
		f.ExecServer = ssmServer
		execInsecureStream = true
		if role.Coordinates() {
			// The fake scanner is the launcher, the bucket and the DB source in one; the demo's cert-renewer cannot be pulled.
			fakeScanner := fake.NewScanner(estate)
			fakeScanner.FailPull("cert-renewer")
			scanRunner = &scanjob.Runner{Store: st, Launcher: fakeScanner, Bucket: fakeScanner, DB: fakeScanner, Cfg: cfg.Scan, Now: time.Now, Log: log}
		}
		clients, prober = f, f
		go estate.RunRollouts(ctx, fakeRolloutStep)
		// The demo data, the fake IdP and the sign-in belong to the task that serves the API. Every process has its own
		// fake estate (it lives in memory), so a split fake install shows leases, kicks and health, not write flows.
		if role.ServesAPI() {
			if err := devseed.Apply(ctx, st, cfg.OffHours.FakeDemo, notifyDemo...); err != nil {
				return fmt.Errorf("seed demo data: %w", err)
			}
			idp, err := fakeidp.New(fakeidp.Options{BaseURL: cfg.BaseURL})
			if err != nil {
				return fmt.Errorf("start fake IdP: %w", err)
			}
			fakeIdP = idp.Handler()
			log.Info("fake AWS mode: demo data loaded; sign in as sam / " + devseed.Password +
				"; fake IdP at " + strings.TrimRight(cfg.BaseURL, "/") + "/fake-idp/metadata")
		}
	} else {
		real := awsx.NewReal(base, accessRoles(role, storeRoles{st}))
		clients, prober = real, real
		if cfg.Scan.Enabled && role.Coordinates() {
			// The scan loop uses the server's own AWS config, never an access role.
			home := awsx.NewHome(base, awsx.HomeConfig{ClusterARN: cfg.Scan.ClusterARN, TaskDefinitionARN: cfg.Scan.TaskDefinitionARN,
				SecurityGroup: cfg.Scan.SecurityGroup, Bucket: cfg.Scan.Bucket, Subnets: cfg.Scan.Subnets})
			source := scanjob.NewHTTPDBSource(&http.Client{}, grypeLatestURL)
			source.Log = log
			scanRunner = &scanjob.Runner{Store: st, Launcher: home, Bucket: home, DB: source, Cfg: cfg.Scan, Now: time.Now, Log: log}
		}
		if role.ServesAPI() {
			if err := auth.Bootstrap(ctx, st, awsx.NewSecretReader(base), cfg.BootstrapAdminSecretARN, log); err != nil {
				return err
			}
		}
	}
	// One applier serves the API (manual wake and sleep) and the reconciler (the leader's loop); both change AWS
	// through the same code under the same per-service lock.
	offHoursApplier := &offhours.Applier{Store: st, Clients: clients, Now: time.Now, Log: log}
	offHoursReconciler := &offhours.Reconciler{Applier: offHoursApplier, Store: st, Interval: cfg.OffHours.Interval, Now: time.Now, Log: log}
	// Notifications: the guarded senders and the leader-only dispatcher. Without a session key the dispatcher fails
	// any delivery that needs a secret with a clear note. In fake mode an in-process receiver at /fake-notify/ takes
	// every delivery.
	notifySenders := &notify.Senders{
		BaseURL: cfg.BaseURL, Version: version, SMTP: cfg.Notify.SMTP, Log: log,
		Guard: safedial.Policy{AllowedPrivate: cfg.Notify.AllowedPrivateCIDRs},
	}
	if fakeAWS {
		fakeNotifySink := fakesink.New()
		fakeNotify = fakeNotifySink.Handler()
		notifySenders.Guard.AllowLoopback = true
		notifySenders.FakeBase = strings.TrimRight(cfg.BaseURL, "/") + "/fake-notify"
		notifySenders.TelegramBase = notifySenders.FakeBase + "/telegram"
		notifySenders.FakeMail = fakeNotifySink.Mail
	}
	notifyDispatcher := &notify.Dispatcher{Store: st, Senders: notifySenders, Box: notifyBox, BaseURL: cfg.BaseURL,
		Interval: cfg.Notify.Interval, Settle: cfg.Notify.Settle, Now: time.Now, Log: log, Telemetry: telemetry.Default}
	telemetry.Default.SetBuildInfo(version) // plan 06

	// What each role serves: the API and web app (all, web), or only /healthz and /metrics (poller, coordinator).
	// handler is built after the background parts so a poller's health can read its lease manager.
	var apiServer *api.Server
	// Kicks cross tasks on the signal bus: a web task publishes, the coordinator that holds the lease listens. In a single
	// task (role all, no shared store) the bus is memory and both sides are this process.
	kicks := &kickPublisher{bus: newKickBus(kvHealth), log: log}
	// Pages that are open in a visible tab: the shared store's registry when there is one, so a web task and the poller
	// that owns the slice see the same set, and a registry inside this process otherwise (role all shares it between the
	// API and the poller).
	focusRegistry := buildFocusRegistry(kvHealth)
	if role.ServesAPI() {
		setup, err := auth.NewSetup(ctx, st, log)
		if err != nil {
			return err
		}
		var scanKicker api.ScanKicker // stays a nil interface when scanning is off
		if cfg.Scan.Enabled {
			scanKicker = kicks
		}
		var storeHealth interface{ Up() bool } // stays a nil interface when there is no shared store
		if kvHealth != nil {
			storeHealth = kvHealth
		}
		apiServer = api.New(api.Server{
			Store: st, Auth: auth.NewService(st, cfg), Setup: setup,
			Clients: clients, Prober: prober, Cfg: cfg, Log: log, Now: time.Now,
			Deploy: &deploy.Service{Store: st, Clients: clients, Scaling: st, MaxDesiredCount: cfg.MaxDesiredCount, Now: time.Now,
				Guard: &offhours.Guard{Store: st, Now: time.Now}},
			ExecAllowInsecureStream: execInsecureStream,
			Limits:                  buildConcurrency(kvHealth),
			Rates:                   buildRates(kvHealth),
			FakeIdP:                 fakeIdP,
			FakeNotify:              fakeNotify,
			MetricsToken:            cfg.MetricsToken, // plan 06
			Telemetry:               telemetry.Default,
			Scan:                    scanKicker,
			OffHours:                offHoursApplier,
			Notify:                  notifySenders,
			NotifyBox:               notifyBox,
			KVHealth:                storeHealth,
			KickOffHours:            kicks.KickOffHours,
			KickNotify:              kicks.KickNotify,
			Focus:                   focusRegistry,
			Bus:                     kicks.bus,
		})
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}

	// The lease manager runs in every role but web: slice loops for role poller, the run-once jobs for role
	// coordinator, both for all. A web task builds none of it, so it cannot poll, launch a scan or run a job.
	pollCtx, stopPoller := context.WithCancel(ctx)
	pollerDone := make(chan struct{})
	var pollerInstance *poller.Poller
	if role.Polls() || role.Coordinates() {
		pollerConfig := poller.ConfigFrom(cfg)
		pollerConfig.Version = version
		pollerConfig.NoSlices = !role.Polls()
		pollerConfig.Registry, pollerConfig.Bus = focusRegistry, kicks.bus
		pollerConfig.Cache = buildSnapshotCache(kvHealth, cfg)
		switch role {
		case config.RolePoller:
			pollerConfig.Roles = []string{"poller"}
		case config.RoleCoordinator:
			pollerConfig.Roles = []string{"coordinator"}
		}
		var coordinatorRunner *coordinator.Runner
		if role.Coordinates() {
			// The jobs that must have one active instance run under the coordinator lease; the deployment outcome logger is
			// one of them (its claim in FinishDeployment already prevents duplicate lines, so a handover loses nothing).
			coordinatorRunner = &coordinator.Runner{
				Store: st, Timing: lease.DefaultTiming(),
				Scan: scanRunner, OffHours: offHoursReconciler, ImagePlatforms: imagePlatformResolver(cfg, st, clients, fakeAWS, log),
				Notify: notifyDispatcher,
				Rollups: &rollup.Runner{Store: st, Clients: clients, Now: time.Now, UtilEnabled: cfg.Analytics.Util,
					SampleInterval: cfg.Poll.CountsHistory, CountsRetention: cfg.Retention.CountsHistory, Log: log},
				Retention: coordinator.RetentionFrom(cfg), RetentionEvery: coordinator.DefaultRetentionEvery,
				Outcomes: deploy.NewOutcomeLogger(st, 5*time.Second),
				Bus:      kicks.bus,
				Log:      log,
			}
			pollerConfig.Coordinator = coordinatorRunner
		}
		pollerInstance = poller.New(st, clients, pollerConfig, log)
		if coordinatorRunner != nil {
			coordinatorRunner.Idle = pollerInstance.IsIdle
		}
		go func() {
			defer close(pollerDone)
			if err := pollerInstance.Run(pollCtx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("poller stopped", "error", err)
			}
		}()
	} else {
		close(pollerDone)
	}

	var handler http.Handler
	if apiServer != nil {
		handler = apiServer.Handler()
	} else {
		var lastTick func() time.Time
		if role == config.RolePoller {
			// ECS replaces a poller whose lease manager has stopped finishing ticks. Other roles stay as they are: role
			// all must not change for a single-task install, and a coordinator has no slices to starve.
			lastTick = func() time.Time {
				if m := pollerInstance.Manager(); m != nil {
					return m.LastTick()
				}
				return time.Time{}
			}
		}
		handler = api.OpsHandler(st.Ping, leaseLoopHealthy(lastTick, time.Now(), time.Now, leaseLoopStaleAfter), cfg.MetricsToken, telemetry.Default, log)
	}
	httpServer := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	if apiServer != nil {
		// Shutdown drains in-flight requests; only running log searches are told to stop (they send StopQuery and
		// audit on detached contexts), so deploys, writes, exec and tails keep their full drain.
		httpServer.RegisterOnShutdown(apiServer.BeginDrain)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()
	log.Info("listening", "addr", listener.Addr().String(), "fake_aws", fakeAWS)

	var result error
	select {
	case <-ctx.Done():
	case result = <-serveErr:
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && result == nil {
		result = err
	}
	stopPoller()
	<-pollerDone
	if errors.Is(result, http.ErrServerClosed) {
		result = nil
	}
	return result
}

// leaseLoopHealthy reports an error when the lease manager's last completed tick is older than limit. A manager that has
// not ticked yet counts from started. A nil lastTick (a role that does not use this check) is always healthy.
func leaseLoopHealthy(lastTick func() time.Time, started time.Time, now func() time.Time, limit time.Duration) func() error {
	return func() error {
		if lastTick == nil {
			return nil
		}
		last := lastTick()
		if last.IsZero() {
			last = started
		}
		if age := now().Sub(last); age > limit {
			return fmt.Errorf("lease manager has not completed a tick for %s", age.Round(time.Second))
		}
		return nil
	}
}

// imagePlatformResolver builds the leader job behind the ARM64 advisor, or nil when HM_ARM64_CHECK is off. With --fake-aws
// the public registries are answered from memory and no socket is opened. Otherwise they are read anonymously through
// the dial-time address guard (public addresses only), and HM_ARM64_PUBLIC_REGISTRIES=false leaves them unchecked.
func imagePlatformResolver(cfg config.Config, st *store.Store, clients awsx.Clients, fakeAWS bool, log *slog.Logger) *platforms.Resolver {
	if !cfg.ARM64.Check {
		return nil
	}
	resolver := &platforms.Resolver{
		Store: st, Clients: clients, Interval: cfg.ARM64.CheckInterval, Recheck: cfg.ARM64.Recheck, Now: time.Now, Log: log,
		OnPublicRead: telemetry.Default.CountRegistryRequest,
	}
	switch {
	case fakeAWS:
		resolver.Public = fake.NewRegistry()
	case cfg.ARM64.PublicRegistries:
		resolver.Public = &registry.Client{HTTP: registry.NewHTTPClient(safedial.Policy{}, nil)}
	}
	return resolver
}
