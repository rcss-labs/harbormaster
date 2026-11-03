package deploy_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// summary renders items as "rev status by" for compact comparison.
func summary(list deploy.RevisionList) []string {
	var out []string
	for _, r := range list.Items {
		out = append(out, fmt.Sprintf("%d %s %s", r.Revision, r.Status, r.RegisteredBy))
	}
	return out
}

func TestRevisions_StatusesAndRegisteredBy(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.3")
	if _, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}
	list, err := h.svc.Revisions(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"43 not_deployed pavel", "42 deployed terraform", "41 previous terraform", "40 previous terraform"}
	if got := summary(list); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("revisions = %v, want %v", got, want)
	}
	if list.DeployedRevision != 42 || list.RollingOutRevision != 0 {
		t.Fatalf("deployed %d rolling out %d", list.DeployedRevision, list.RollingOutRevision)
	}
	first := list.Items[0]
	if first.ImageTag != "1.18.3" || first.CPU == "" || first.Memory == "" || first.RegisteredAt.IsZero() || !strings.HasSuffix(first.ARN, ":43") {
		t.Fatalf("revision 43 = %+v", first)
	}
}

func TestRevisions_RollingOut(t *testing.T) {
	h := newHarness(t)
	list, err := h.svc.Revisions(t.Context(), notificationsKey)
	if err != nil {
		t.Fatal(err)
	}
	got := summary(list)
	if len(got) < 2 || got[0] != "10 rolling_out terraform" || got[1] != "9 deployed terraform" {
		t.Fatalf("revisions = %v", got)
	}
	if list.DeployedRevision != 9 || list.RollingOutRevision != 10 {
		t.Fatalf("deployed %d rolling out %d", list.DeployedRevision, list.RollingOutRevision)
	}
}

func TestRevisions_ExactFamilyOnly(t *testing.T) {
	h := newHarness(t)
	h.fake.Estate.Mu.Lock()
	var template ecstypes.TaskDefinition
	for _, td := range h.fake.Estate.TaskDefinitions {
		if aws.ToString(td.Definition.Family) == "payments-api" && td.Definition.Revision == 42 {
			template = td.Definition
		}
	}
	for _, family := range []string{"payments-api-worker", "payments-api-batch"} {
		copied := template
		copied.Family = aws.String(family)
		copied.Revision = 1
		copied.TaskDefinitionArn = aws.String(strings.Replace(aws.ToString(template.TaskDefinitionArn), "payments-api:42", family+":1", 1))
		h.fake.Estate.TaskDefinitions = append(h.fake.Estate.TaskDefinitions, fake.TaskDefinition{AccountID: homelabAccountID, Region: homelabRegion, Definition: copied})
	}
	h.fake.Estate.Mu.Unlock()

	list, err := h.svc.Revisions(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Items {
		if strings.Contains(r.ARN, "worker") || strings.Contains(r.ARN, "batch") {
			t.Fatalf("revision of another family listed: %+v", r)
		}
	}
	if len(list.Items) != 3 {
		t.Fatalf("revisions = %v, want 3 payments-api revisions", summary(list))
	}
}

func TestRevisions_DeployedInactiveStillListed(t *testing.T) {
	h := newHarness(t)
	h.fake.Estate.Mu.Lock()
	for i := range h.fake.Estate.TaskDefinitions {
		td := &h.fake.Estate.TaskDefinitions[i]
		if aws.ToString(td.Definition.Family) == "payments-api" && td.Definition.Revision == 42 {
			td.Definition.Status = ecstypes.TaskDefinitionStatusInactive
		}
	}
	h.fake.Estate.Mu.Unlock()
	list, err := h.svc.Revisions(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	got := summary(list)
	if len(got) < 1 || got[0] != "42 deployed terraform" {
		t.Fatalf("revisions = %v, want the INACTIVE deployed :42 listed as deployed", got)
	}
}

func TestRevisions_CachedFor30s(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	h.svc.Now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if _, err := h.svc.Revisions(t.Context(), paymentsKey); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.fake.Calls()["ListTaskDefinitions"]; n != 1 {
		t.Fatalf("ListTaskDefinitions calls = %d within 30 s, want 1", n)
	}
	now = now.Add(31 * time.Second)
	if _, err := h.svc.Revisions(t.Context(), paymentsKey); err != nil {
		t.Fatal(err)
	}
	if n := h.fake.Calls()["ListTaskDefinitions"]; n != 2 {
		t.Fatalf("ListTaskDefinitions calls = %d after 31 s, want 2", n)
	}
}

func TestRevisions_RegisterEvictsCache(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.Revisions(t.Context(), paymentsKey); err != nil {
		t.Fatal(err)
	}
	draft := draftFromDeployed(t, h, "482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.3")
	if _, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}
	list, err := h.svc.Revisions(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(list); len(got) == 0 || got[0] != "43 not_deployed pavel" {
		t.Fatalf("revisions right after Register = %v, want :43 first", got)
	}
}

func TestRevisions_RolloutWithInactiveDeployedRevisionStillListed(t *testing.T) {
	h := newHarness(t)
	h.fake.Estate.Mu.Lock()
	for i := range h.fake.Estate.TaskDefinitions {
		td := &h.fake.Estate.TaskDefinitions[i]
		if aws.ToString(td.Definition.Family) == "notifications" && td.Definition.Revision == 9 {
			td.Definition.Status = ecstypes.TaskDefinitionStatusInactive
		}
	}
	h.fake.Estate.Mu.Unlock()
	list, err := h.svc.Revisions(t.Context(), notificationsKey)
	if err != nil {
		t.Fatal(err)
	}
	got := summary(list)
	if len(got) < 2 || got[0] != "10 rolling_out terraform" || got[1] != "9 deployed terraform" {
		t.Fatalf("revisions = %v, want :10 rolling_out and the INACTIVE :9 deployed", got)
	}
}

func TestRevisions_ThrottleSurfaces(t *testing.T) {
	h := newHarness(t)
	h.fake.FailNext("ListTaskDefinitions", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"})
	_, err := h.svc.Revisions(t.Context(), paymentsKey)
	if !awsx.IsThrottle(err) {
		t.Fatalf("err = %v, want a throttle error", err)
	}
}

// addTaskDefinitionCopy adds a copy of payments-api:42 as family:revision to the fake estate,
// after the poller has run, so the store does not know about it.
func addTaskDefinitionCopy(t *testing.T, h *harness, family string, revision int) {
	t.Helper()
	h.fake.Estate.Mu.Lock()
	defer h.fake.Estate.Mu.Unlock()
	var template ecstypes.TaskDefinition
	for _, td := range h.fake.Estate.TaskDefinitions {
		if aws.ToString(td.Definition.Family) == "payments-api" && td.Definition.Revision == 42 {
			template = td.Definition
		}
	}
	copied := template
	copied.Family = aws.String(family)
	copied.Revision = int32(revision)
	copied.TaskDefinitionArn = aws.String(strings.Replace(aws.ToString(template.TaskDefinitionArn), "payments-api:42", fmt.Sprintf("%s:%d", family, revision), 1))
	h.fake.Estate.TaskDefinitions = append(h.fake.Estate.TaskDefinitions, fake.TaskDefinition{AccountID: homelabAccountID, Region: homelabRegion, Definition: copied})
}

func TestRevisionDefinition_FromStore(t *testing.T) {
	h := newHarness(t)
	draft := draftFromDeployed(t, h, "482109375512.dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.3")
	if _, err := h.svc.Register(t.Context(), h.pavel, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}
	newest, err := h.svc.RevisionDefinition(t.Context(), paymentsKey, 43)
	if err != nil {
		t.Fatal(err)
	}
	if newest.Revision != 43 || !strings.HasSuffix(newest.ARN, "task-definition/payments-api:43") ||
		newest.RegisteredBy != "pavel" || newest.RegisteredAt.IsZero() {
		t.Fatalf("revision 43 = %+v", newest)
	}
	encoded, err := json.Marshal(newest.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "payments-api:1.18.3") {
		t.Fatalf("definition does not carry the registered image: %s", encoded)
	}
	if _, has := newest.Definition["taskDefinitionArn"]; has {
		t.Fatal("definition is not register-ready: taskDefinitionArn present")
	}

	deployed, err := h.svc.DeployedDefinition(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	older, err := h.svc.RevisionDefinition(t.Context(), paymentsKey, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(older.Definition, deployed.Definition) || older.RegisteredBy != "terraform" {
		t.Fatalf("revision 42 = %+v, want the deployed definition registered by terraform", older)
	}
}

func TestRevisionDefinition_DescribesWhenMissing(t *testing.T) {
	h := newHarness(t)
	addTaskDefinitionCopy(t, h, "payments-api", 44)
	before := h.fake.Calls()["DescribeTaskDefinition"]

	got, err := h.svc.RevisionDefinition(t.Context(), paymentsKey, 44)
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != 44 || !strings.HasSuffix(got.ARN, "payments-api:44") || got.Definition["family"] != "payments-api" {
		t.Fatalf("revision 44 = %+v", got)
	}
	if calls := h.fake.Calls()["DescribeTaskDefinition"] - before; calls != 1 {
		t.Fatalf("DescribeTaskDefinition calls = %d, want 1 for a revision the store does not hold", calls)
	}
}

func TestRevisionDefinition_OtherFamily404(t *testing.T) {
	h := newHarness(t)
	addTaskDefinitionCopy(t, h, "payments-api-worker", 1)
	for _, revision := range []int{0, 1, 99} {
		got, err := h.svc.RevisionDefinition(t.Context(), paymentsKey, revision)
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("revision %d: err = %v with %+v, want store.ErrNotFound", revision, err, got)
		}
	}
}
