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
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// grypeLatestURL is Anchore's listing of the current Grype vulnerability database.
const grypeLatestURL = "https://grype.anchore.io/databases/v6/latest.json"

const (
	shutdownTimeout = 15 * time.Second
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
	st, err := store.Open(ctx, dbURL)
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
	if err := flags.Parse(args); err != nil {
		return errUsage{"usage: harbormaster serve [--fake-aws]"}
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	fakeAWS := *fakeFlag || cfg.FakeAWS
	// The API's SSRF filter relaxes http loopback URLs only when cfg.FakeAWS is set, so the --fake-aws
	// flag must set it too or the demo IdP's metadata cannot be imported.
	cfg.FakeAWS = fakeAWS
	// Fake mode always has image scanning on, backed by the fake scanner, so the demo and the e2e suite need no variables.
	if fakeAWS {
		cfg.Scan.Enabled = true
	}
	if err := cfg.ValidateScanLaunch(); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))

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

	st, err := openStore(ctx, dbURL)
	if err != nil {
		return err
	}
	defer st.Close()
	if applied, err := st.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	} else if len(applied) > 0 {
		log.Info("applied migrations", "versions", applied)
	}

	var (
		clients awsx.Clients
		prober  awsx.Prober
		// plan 04: terminal
		execInsecureStream bool
		// plan 07: the fake IdP exists only in fake AWS mode; nil leaves /fake-idp/ unrouted.
		fakeIdP http.Handler
		// scanRunner is the image scan loop; nil when scanning is off.
		scanRunner *scanjob.Runner
	)
	if fakeAWS {
		estate := fake.MockupEstate(time.Now)
		f := fake.New(estate)
		// The fake ECS hands out ws:// streams served by an in-process fake SSM agent.
		ssmServer := fake.NewSSMServer(fake.SSMServerOptions{})
		ssmServer.Start()
		defer ssmServer.Close()
		f.ExecServer = ssmServer
		execInsecureStream = true
		// The fake scanner is the launcher, the bucket and the DB source in one; the demo's cert-renewer cannot be pulled.
		fakeScanner := fake.NewScanner(estate)
		fakeScanner.FailPull("cert-renewer")
		scanRunner = &scanjob.Runner{Store: st, Launcher: fakeScanner, Bucket: fakeScanner, DB: fakeScanner, Cfg: cfg.Scan, Now: time.Now, Log: log}
		clients, prober = f, f
		go estate.RunRollouts(ctx, fakeRolloutStep)
		if err := devseed.Apply(ctx, st); err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
		idp, err := fakeidp.New(fakeidp.Options{BaseURL: cfg.BaseURL})
		if err != nil {
			return fmt.Errorf("start fake IdP: %w", err)
		}
		fakeIdP = idp.Handler()
		log.Info("fake AWS mode: demo data loaded; sign in as sam / " + devseed.Password +
			"; fake IdP at " + strings.TrimRight(cfg.BaseURL, "/") + "/fake-idp/metadata")
	} else {
		real := awsx.NewReal(base, storeRoles{st})
		clients, prober = real, real
		if cfg.Scan.Enabled {
			// The scan loop uses the server's own AWS config, never an access role.
			home := awsx.NewHome(base, awsx.HomeConfig{ClusterARN: cfg.Scan.ClusterARN, TaskDefinitionARN: cfg.Scan.TaskDefinitionARN,
				SecurityGroup: cfg.Scan.SecurityGroup, Bucket: cfg.Scan.Bucket, Subnets: cfg.Scan.Subnets})
			source := scanjob.NewHTTPDBSource(&http.Client{}, grypeLatestURL)
			source.Log = log
			scanRunner = &scanjob.Runner{Store: st, Launcher: home, Bucket: home, DB: source, Cfg: cfg.Scan, Now: time.Now, Log: log}
		}
		if err := auth.Bootstrap(ctx, st, awsx.NewSecretReader(base), cfg.BootstrapAdminSecretARN, log); err != nil {
			return err
		}
	}
	setup, err := auth.NewSetup(ctx, st, log)
	if err != nil {
		return err
	}

	telemetry.Default.SetBuildInfo(version) // plan 06
	var scanKicker api.ScanKicker           // stays a nil interface when scanning is off
	if scanRunner != nil {
		scanKicker = scanRunner
	}
	server := api.New(api.Server{
		Store: st, Auth: auth.NewService(st, cfg), Setup: setup,
		Clients: clients, Prober: prober, Cfg: cfg, Log: log, Now: time.Now,
		Deploy:                  &deploy.Service{Store: st, Clients: clients, Scaling: st, MaxDesiredCount: cfg.MaxDesiredCount, Now: time.Now},
		ExecAllowInsecureStream: execInsecureStream,
		FakeIdP:                 fakeIdP,
		MetricsToken:            cfg.MetricsToken, // plan 06
		Telemetry:               telemetry.Default,
		Scan:                    scanKicker,
	})
	httpServer := &http.Server{Addr: cfg.Listen, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}

	pollerConfig := poller.ConfigFrom(cfg)
	pollerConfig.Scan = scanRunner
	pollCtx, stopPoller := context.WithCancel(ctx)
	pollerDone := make(chan struct{})
	go func() {
		defer close(pollerDone)
		if err := poller.New(st, clients, pollerConfig, log).Run(pollCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("poller stopped", "error", err)
		}
	}()

	// Every task runs the outcome logger; the atomic claim in FinishDeployment prevents duplicate lines.
	go deploy.NewOutcomeLogger(st, 5*time.Second).Run(ctx)

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
