package api

import (
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	homelabAccountID = "482109375512"
	stagingAccountID = "731022849906"
)

// seedMockupTags creates the mockup tags, assigns core-services to Prod and staging-web to Staging,
// and grants omer, oncall and dana their mockup permissions. Users oncall and dana are created here.
func seedMockupTags(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := t.Context()
	prod, err := st.CreateTag(ctx, "Prod", "red")
	if err != nil {
		t.Fatal(err)
	}
	staging, err := st.CreateTag(ctx, "Staging", "amber")
	if err != nil {
		t.Fatal(err)
	}
	dev, err := st.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetClusterTag(ctx, model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, prod.ID))
	must(st.SetClusterTag(ctx, model.ClusterKey{AccountID: stagingAccountID, Region: "us-east-1", Cluster: "staging-web"}, staging.ID))

	grant := func(username string, perms map[authz.TagID]authz.ActionSet) {
		u, err := st.GetUserByUsername(ctx, username)
		if err != nil {
			u, err = st.CreateUser(ctx, username, "unused-hash", false, false)
			must(err)
		}
		for tag, set := range perms {
			must(st.SetPermission(ctx, u.ID, tag, set))
		}
	}
	view, viewLogsOperateTerminal := authz.ActionSet(authz.View), authz.ActionSet(authz.View|authz.Logs|authz.Operate|authz.Terminal)
	viewLogs := authz.ActionSet(authz.View | authz.Logs)
	grant("omer", map[authz.TagID]authz.ActionSet{dev.ID: authz.Presets["Operator"], staging.ID: view})
	grant("oncall", map[authz.TagID]authz.ActionSet{staging.ID: viewLogsOperateTerminal, prod.ID: viewLogsOperateTerminal})
	grant("dana", map[authz.TagID]authz.ActionSet{dev.ID: authz.Presets["Full"], staging.ID: authz.Presets["Deployer"], prod.ID: viewLogs})
}

// pollAll runs one full poll cycle for every account x region of the fake estate.
func pollAll(t *testing.T, st *store.Store, f *fake.Fake) {
	t.Helper()
	for _, account := range f.Estate.Accounts {
		for _, region := range account.Regions {
			target := awsx.Target{AccountID: account.ID, Region: region}
			if err := poller.PollOnce(t.Context(), st, f, poller.Config{MaxInFlight: 4}, target); err != nil {
				t.Fatalf("poll %v: %v", target, err)
			}
		}
	}
}

// expectedReserved sums cpu/1024 and memory/1024 over the estate's RUNNING tasks of one cluster.
func expectedReserved(f *fake.Fake, cluster string) (vcpu, gib float64, running int) {
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	for _, task := range f.Estate.Tasks {
		if task.ClusterName != cluster || aws.ToString(task.Task.LastStatus) != "RUNNING" {
			continue
		}
		cpu, _ := strconv.Atoi(aws.ToString(task.Task.Cpu))
		mem, _ := strconv.Atoi(aws.ToString(task.Task.Memory))
		vcpu += float64(cpu) / 1024
		gib += float64(mem) / 1024
		running++
	}
	return vcpu, gib, running
}
