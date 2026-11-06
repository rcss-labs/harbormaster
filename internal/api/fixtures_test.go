package api

import (
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/poller"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	homelabAccountID = "482109375512"
	stagingAccountID = "731022849906"
)

// seedMockupTags loads the shared dev seed (tags, users, permissions, cluster tags). Existing
// users such as sam and omer keep their testPassword; dana and oncall get the dev-seed password.
func seedMockupTags(t *testing.T, st *store.Store) {
	t.Helper()
	if err := devseed.ForceApply(t.Context(), st); err != nil {
		t.Fatal(err)
	}
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

// setPassword gives an existing seeded user testPassword so tests can log in as them.
func setPassword(t *testing.T, st *store.Store, username string) {
	t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.GetUserByUsername(t.Context(), username)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword(t.Context(), u.ID, hash, false); err != nil {
		t.Fatal(err)
	}
}

// seedAccounts connects the two mockup AWS accounts so account names resolve in responses.
func seedAccounts(t *testing.T, st *store.Store) {
	t.Helper()
	for _, a := range []store.AWSAccount{
		{Name: "homelab-admin", AccountID: homelabAccountID, RoleARN: "arn:aws:iam::" + homelabAccountID + ":role/harbormaster-access", ExternalID: "x", Regions: []string{"us-east-1", "eu-west-1", "eu-central-1"}, Enabled: true},
		{Name: "staging-example", AccountID: stagingAccountID, RoleARN: "arn:aws:iam::" + stagingAccountID + ":role/harbormaster-access", ExternalID: "x", Regions: []string{"us-east-1"}, Enabled: true},
	} {
		if _, err := st.CreateAccount(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
}
