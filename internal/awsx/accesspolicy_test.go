package awsx_test

import (
	"context"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/platforms"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/rollup"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

const accessModuleMain = "../../terraform/modules/harbormaster-access/main.tf"

// iamPrefix maps the fake's service names (see begin in fake.go) to IAM action prefixes.
var iamPrefix = map[string]string{
	"ecs": "ecs", "ecr": "ecr", "ec2": "ec2", "efs": "elasticfilesystem", "elasticloadbalancing": "elasticloadbalancing",
	"application-autoscaling": "application-autoscaling", "cloudwatch": "cloudwatch", "logs": "logs", "ssm": "ssm",
}

// policyActions returns the quoted actions between "# policy: <name>" and "# end policy" in the access module.
func policyActions(t *testing.T, name string) []string {
	t.Helper()
	src, err := os.ReadFile(accessModuleMain)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	inside := false
	quoted := regexp.MustCompile(`"([a-z0-9-]+:[A-Za-z*]+)"`)
	for _, line := range strings.Split(string(src), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "# policy: "+name:
			inside = true
		case trimmed == "# end policy":
			inside = false
		case inside && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "variable"):
			for _, m := range quoted.FindAllStringSubmatch(line, -1) {
				actions = append(actions, m[1])
			}
		}
	}
	if len(actions) == 0 {
		t.Fatalf("%s has no actions between %q and %q: the %s policy is missing", accessModuleMain, "# policy: "+name, "# end policy", name)
	}
	return actions
}

// callActions turns the fake's "account|region|service|operation" counters into the IAM actions they need.
func callActions(t *testing.T, counts map[string]uint64) []string {
	t.Helper()
	set := map[string]bool{}
	for key := range counts {
		parts := strings.Split(key, "|")
		if len(parts) != 4 {
			t.Fatalf("call key %q is not account|region|service|operation", key)
		}
		prefix, ok := iamPrefix[parts[2]]
		if !ok {
			t.Fatalf("call key %q: service %q has no IAM prefix in this test", key, parts[2])
		}
		set[prefix+":"+parts[3]] = true
	}
	out := make([]string, 0, len(set))
	for a := range set {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// granted reports whether any pattern (an IAM action, maybe ending in *) allows the action.
func granted(patterns []string, action string) bool {
	for _, p := range patterns {
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(action)); ok {
			return true
		}
	}
	return false
}

func TestAccessPolicies_CoverEachRolesCallsAndNothingBroader(t *testing.T) {
	// The jobs write to the store, so this test needs a database; it skips without one, like every DB-backed
	// test. The structure tests in accesspolicy_structure_test.go need no database and always run.
	if os.Getenv("HM_TEST_DATABASE_URL") == "" {
		t.Skip("HM_TEST_DATABASE_URL not set")
	}
	pollerPolicy := policyActions(t, "poller")
	coordinatorPolicy := policyActions(t, "coordinator")

	const account = "482109375512"
	ctx := context.Background()
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// Saturday 2026-11-07 23:00 Berlin: a Mon-Fri 07-20 schedule has the service asleep.
	now := time.Date(2026, 11, 7, 23, 0, 0, 0, berlin)
	clock := func() time.Time { return now }

	st := storetest.Open(t)
	fk := fake.New(fake.MockupEstate(clock))
	payments := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: account, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
	fk.Estate.Mu.Lock()
	fk.Estate.ScalingConfig[payments] = &fake.ScalingSeed{Min: 2, Max: 6}
	fk.Estate.Mu.Unlock()
	fk.Estate.SetImageIndex("catalog-api", true) // the resolver reads its manifest with BatchGetImage

	if _, err := st.CreateAccount(ctx, store.AWSAccount{Name: "homelab", AccountID: account,
		RoleARN: "arn:aws:iam::" + account + ":role/x", ExternalID: "e", Regions: []string{"us-east-1", "eu-west-1", "eu-central-1"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, "sam", "hash", true, false)
	if err != nil {
		t.Fatal(err)
	}
	awake := []byte(strings.Repeat("0", 168))
	for day := 0; day < 5; day++ {
		for hour := 7; hour < 20; hour++ {
			awake[day*24+hour] = '1'
		}
	}
	if _, err := st.CreateOffHoursSchedule(ctx, store.OffHoursSchedule{Name: "staging nights", Enabled: true,
		Selectors: []store.OffHoursSelector{{Kind: "aws", Key: "env", Value: "staging"}}, Timezone: "Europe/Berlin", Awake: string(awake)}, user.ID); err != nil {
		t.Fatal(err)
	}

	// The poller: one full pass of every tier, with the counts-history tick, for each account x region.
	fk.ResetCallCounts()
	for _, region := range []string{"us-east-1", "eu-west-1", "eu-central-1"} {
		if err := poller.PollCountsOnce(ctx, st, fk, poller.Config{MaxInFlight: 4}, awsx.Target{AccountID: account, Region: region}); err != nil {
			t.Fatalf("poll %s: %v", region, err)
		}
	}
	pollerCalls := callActions(t, fk.CallCounts())

	// The coordinator: two services that off-hours puts to sleep and then wakes (payments-api has a scalable target;
	// notifications has none, so Harbormaster registers its own and later deregisters it), the image platform
	// resolver on an ECR image, and the utilisation rollup.
	notifications := model.ServiceKey{ClusterKey: payments.ClusterKey, Service: "notifications"}
	polls := 0
	tagServices := func(env string, desired int) {
		t.Helper()
		polls++
		for _, key := range []model.ServiceKey{payments, notifications} {
			// The store writes a service row only when a hashed field changed, so the desired count varies per call.
			if _, err := st.UpsertServices(ctx, []model.Service{{Key: key, Status: "ACTIVE", Desired: desired + polls, LaunchType: "FARGATE",
				Raw: []byte(`{"Tags":[{"Key":"env","Value":"` + env + `"}]}`), PolledAt: now}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	tagServices("staging", 3)
	fk.ResetCallCounts()
	reconciler := &offhours.Reconciler{Applier: &offhours.Applier{Store: st, Clients: fk, Now: clock}, Store: st, Interval: time.Minute, Now: clock}
	if err := reconciler.Cycle(ctx); err != nil { // enrols and puts it to sleep
		t.Fatalf("off-hours sleep cycle: %v", err)
	}
	now = time.Date(2026, 11, 9, 10, 0, 0, 0, berlin) // Monday morning
	tagServices("staging", 0)
	if err := reconciler.Cycle(ctx); err != nil { // wakes them
		t.Fatalf("off-hours wake cycle: %v", err)
	}
	tagServices("dev", 3)
	if err := reconciler.Cycle(ctx); err != nil { // the tag no longer matches: both leave off-hours
		t.Fatalf("off-hours release cycle: %v", err)
	}
	resolver := &platforms.Resolver{Store: st, Clients: fk, Interval: time.Minute, Recheck: 24 * time.Hour, Now: clock}
	if err := resolver.RunOnce(ctx); err != nil {
		t.Fatalf("image platform resolver: %v", err)
	}
	rollups := &rollup.Runner{Store: st, Clients: fk, Now: func() time.Time { return now.UTC() }, UtilEnabled: true,
		SampleInterval: 5 * time.Minute, CountsRetention: 168 * time.Hour}
	if _, err := rollups.RunDue(ctx); err != nil {
		t.Fatalf("rollups: %v", err)
	}
	coordinatorCalls := callActions(t, fk.CallCounts())

	// Every call the code makes is allowed.
	for _, a := range pollerCalls {
		if !granted(pollerPolicy, a) {
			t.Errorf("the poller calls %s, which the poller policy %v does not allow", a, pollerPolicy)
		}
	}
	for _, a := range coordinatorCalls {
		if !granted(coordinatorPolicy, a) {
			t.Errorf("a coordinator job calls %s, which the coordinator policy %v does not allow", a, coordinatorPolicy)
		}
	}

	// The runs exercised what they were meant to, so the check above is not vacuous.
	for _, want := range []string{"ecs:DescribeServices", "ecs:ListTasks", "ec2:DescribeVpcs", "elasticloadbalancing:DescribeTargetHealth", "application-autoscaling:DescribeScalableTargets"} {
		if !slices.Contains(pollerCalls, want) {
			t.Errorf("the poller pass did not call %s (calls: %v)", want, pollerCalls)
		}
	}
	for _, want := range []string{"ecs:UpdateService", "application-autoscaling:RegisterScalableTarget", "application-autoscaling:DeregisterScalableTarget", "application-autoscaling:PutScheduledAction", "application-autoscaling:DeleteScheduledAction", "ecr:DescribeImages", "ecr:BatchGetImage", "cloudwatch:GetMetricData"} {
		if !slices.Contains(coordinatorCalls, want) {
			t.Errorf("the coordinator jobs did not call %s (calls: %v)", want, coordinatorCalls)
		}
	}

	// Nothing broader.
	for _, a := range pollerPolicy {
		_, verb, _ := strings.Cut(a, ":")
		if !strings.HasPrefix(verb, "List") && !strings.HasPrefix(verb, "Describe") {
			t.Errorf("the poller policy grants %s: only List and Describe actions are allowed", a)
		}
	}
	for _, a := range pollerPolicy {
		if granted([]string{a}, "ecs:ExecuteCommand") || strings.HasPrefix(a, "logs:") || strings.HasPrefix(a, "cloudwatch:") || strings.HasPrefix(a, "ecr:") {
			t.Errorf("the poller policy grants %s: no exec, logs, metrics or ECR", a)
		}
	}
	forbidden := []string{"ecs:ExecuteCommand", "ecs:RegisterTaskDefinition", "ecs:StopTask", "iam:PassRole", "logs:FilterLogEvents", "logs:StartQuery", "logs:StartLiveTail", "ssm:TerminateSession"}
	for _, f := range forbidden {
		if granted(pollerPolicy, f) {
			t.Errorf("the poller policy allows %s", f)
		}
		if granted(coordinatorPolicy, f) {
			t.Errorf("the coordinator policy allows %s", f)
		}
	}
	// Every coordinator action is one a job needs: the policy has no spare permission. The one exception is the
	// service-linked role statement: AWS creates that role on the first RegisterScalableTarget in an account that
	// never used ECS autoscaling and fails the call without the permission, and the fake never makes that call.
	for _, a := range coordinatorPolicy {
		if a == "iam:CreateServiceLinkedRole" {
			continue
		}
		used := false
		for _, c := range coordinatorCalls {
			if granted([]string{a}, c) {
				used = true
				break
			}
		}
		if !used {
			t.Errorf("the coordinator policy grants %s, which no coordinator job called in this run", a)
		}
	}
}
