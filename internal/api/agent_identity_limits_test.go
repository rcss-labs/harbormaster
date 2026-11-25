package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// withIdentityLimits turns the agent on and sets the identity's limits, on top of the token defaults.
func withIdentityLimits(rate, maxGB, dailyGB int, tweak func(*config.Config)) func(*config.Config) {
	return func(c *config.Config) {
		c.AgentURL, c.AgentRunTokenTTL = testAgentURL, 15*time.Minute
		c.AgentIdentityRatePerSecond, c.AgentIdentityLogSearchMaxGB, c.AgentIdentityLogSearchDailyGB = rate, maxGB, dailyGB
		if tweak != nil {
			tweak(c)
		}
	}
}

// identityRunClient creates the identity with View and Logs on every tag and returns a client holding its run token.
func identityRunClient(t *testing.T, env *logSearchEnv) (*apiClient, store.User) {
	t.Helper()
	identity, err := env.st.EnsureAgentIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	sam, err := env.st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := env.st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	perms := map[authz.TagID]authz.ActionSet{}
	for _, tag := range tags {
		perms[tag.ID] = authz.ActionSet(authz.View) | authz.ActionSet(authz.Logs)
	}
	if err := env.st.SetAgentIdentityPermissions(t.Context(), perms, sam.ID); err != nil {
		t.Fatal(err)
	}
	raw, _, err := env.api.Auth.MintAgentRunToken(t.Context(), uuid.New(), true)
	if err != nil {
		t.Fatal(err)
	}
	c := newAPIClient(t, env.srv)
	c.bearer = raw
	return c, identity
}

func usageOf(t *testing.T, env *logSearchEnv, userID int64) int64 {
	t.Helper()
	used, _, err := env.st.TokenLogScanUsage(t.Context(), userID, env.api.now())
	if err != nil {
		t.Fatal(err)
	}
	return used
}

// drain calls whoami until the client is rate limited and returns how many calls it took, or 0 if it never was.
func drain(c *apiClient, max int) int {
	for i := 1; i <= max; i++ {
		resp := c.get("/api/tokens/current")
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			return i
		}
	}
	return 0
}

func TestTokenRequests_IdentityHasItsOwnBucket(t *testing.T) {
	env := newTokenLogSearchEnv(t, withIdentityLimits(1, 2, 20, nil))
	identity, _ := identityRunClient(t, env)
	person := tokenClient(t, env.srv, env.st, "sam")

	// A person with an empty bucket does not slow the identity.
	if drain(person, 300) == 0 {
		t.Fatal("the person's token was never rate limited")
	}
	if resp := identity.get("/api/tokens/current"); resp.StatusCode != http.StatusOK {
		t.Errorf("identity while a person's bucket is empty: status %d, want 200", resp.StatusCode)
	}

	// And the reverse: an empty identity bucket (burst 60 x 1 per second) leaves a person alone.
	at := drain(identity, 300)
	if at == 0 {
		t.Fatal("the identity was never rate limited")
	}
	if at < 55 || at > 70 {
		t.Errorf("identity limited at call %d, want just after its burst of 60", at)
	}
	if resp := tokenClient(t, env.srv, env.st, "omer").get("/api/tokens/current"); resp.StatusCode != http.StatusOK {
		t.Errorf("person while the identity's bucket is empty: status %d, want 200", resp.StatusCode)
	}
}

func TestLogSearch_IdentityUsesItsOwnDailyBudget(t *testing.T) {
	// The identity's day is 1 GiB, a person's token day is 20 GiB; every query reports 1 GiB scanned.
	env := newTokenLogSearchEnv(t, withIdentityLimits(5, 2, 1, nil), scanning(gib))
	identity, row := identityRunClient(t, env)
	sam, _ := env.st.GetUserByUsername(t.Context(), "sam")

	resp := identity.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("identity search: status %d", resp.StatusCode)
	}
	var body tokenSearchBody
	decode(t, resp, &body)
	if body.ScanLimitBytes != gib {
		t.Fatalf("scan_limit_bytes=%d, want the identity's 1 GiB day, not the person's 2 GiB cap", body.ScanLimitBytes)
	}
	if used := usageOf(t, env, row.ID); used < gib {
		t.Errorf("identity ledger = %d, want the search charged to it", used)
	}
	if used := usageOf(t, env, sam.ID); used != 0 {
		t.Errorf("a person's ledger = %d, want 0: the identity must not charge it", used)
	}
	if r := identity.post("/api/log-search", searchReq(paymentsID())); r.StatusCode != http.StatusTooManyRequests {
		t.Errorf("second identity search: status %d, want 429 with its day used up", r.StatusCode)
	}
	// The identity's budget never changes a person's: sam still has the full cap.
	var person tokenSearchBody
	decode(t, tokenClient(t, env.srv, env.st, "sam").post("/api/log-search", searchReq(paymentsID())), &person)
	if person.ScanLimitBytes != 2*gib {
		t.Errorf("person's scan_limit_bytes = %d, want 2 GiB", person.ScanLimitBytes)
	}
}

func TestLogSearch_IdentityUnaffectedByTokenDailyZero(t *testing.T) {
	env := newTokenLogSearchEnv(t, withIdentityLimits(5, 2, 20, func(c *config.Config) { c.TokenLogSearchDailyGB = 0 }))
	identity, _ := identityRunClient(t, env)
	if resp := identity.post("/api/log-search", searchReq(paymentsID())); resp.StatusCode != http.StatusOK {
		t.Errorf("identity search with HM_TOKEN_LOG_SEARCH_DAILY_GB=0: status %d, want 200", resp.StatusCode)
	}
	// The person's token search stays off.
	if resp := tokenClient(t, env.srv, env.st, "sam").post("/api/log-search", searchReq(paymentsID())); resp.StatusCode != http.StatusForbidden {
		t.Errorf("person's token search with the budget at 0: status %d, want 403", resp.StatusCode)
	}
}

func TestLogSearch_IdentityDailyZeroRefuses(t *testing.T) {
	env := newTokenLogSearchEnv(t, withIdentityLimits(5, 2, 0, nil))
	identity, row := identityRunClient(t, env)
	resp := identity.post("/api/log-search", searchReq(paymentsID()))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("identity search with its budget at 0: status %d, want 403", resp.StatusCode)
	}
	if used := usageOf(t, env, row.ID); used != 0 {
		t.Errorf("identity ledger = %d, want nothing charged", used)
	}
	if resp := tokenClient(t, env.srv, env.st, "sam").post("/api/log-search", searchReq(paymentsID())); resp.StatusCode != http.StatusOK {
		t.Errorf("person's token search: status %d, want 200: the identity's 0 must not switch people off", resp.StatusCode)
	}
}

func TestLogSearch_IdentityCapNeverAboveGlobalCap(t *testing.T) {
	for name, tc := range map[string]struct{ identityMax, global int }{
		"global smaller":   {identityMax: 5, global: 1},
		"identity smaller": {identityMax: 1, global: 3},
		"identity no cap":  {identityMax: 0, global: 1},
	} {
		t.Run(name, func(t *testing.T) {
			env := newTokenLogSearchEnv(t, withIdentityLimits(5, tc.identityMax, 20, func(c *config.Config) { c.LogSearchMaxGB = tc.global }))
			identity, _ := identityRunClient(t, env)
			resp := identity.post("/api/log-search", searchReq(paymentsID()))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var body tokenSearchBody
			decode(t, resp, &body)
			want := int64(min(tc.global, tc.identityMax)) * gib
			if tc.identityMax == 0 {
				want = int64(tc.global) * gib
			}
			if body.ScanLimitBytes != want {
				t.Errorf("scan_limit_bytes = %d, want %d", body.ScanLimitBytes, want)
			}
		})
	}
}

func TestWhoAmI_IdentityReportsItsBudget(t *testing.T) {
	env := newTokenLogSearchEnv(t, withIdentityLimits(5, 3, 7, nil))
	identity, _ := identityRunClient(t, env)
	resp := identity.get("/api/tokens/current")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var body WhoAmIDTO
	decode(t, resp, &body)
	if body.LogSearch.DailyBytes != 7*gib || body.LogSearch.MaxBytesPerSearch != 3*gib {
		t.Errorf("identity log_search = %+v, want a 7 GiB day and a 3 GiB cap", body.LogSearch)
	}
	var person WhoAmIDTO
	decode(t, tokenClient(t, env.srv, env.st, "sam").get("/api/tokens/current"), &person)
	if person.LogSearch.DailyBytes != 20*gib || person.LogSearch.MaxBytesPerSearch != 2*gib {
		t.Errorf("person log_search = %+v, want the token limits 20 GiB and 2 GiB", person.LogSearch)
	}
}
