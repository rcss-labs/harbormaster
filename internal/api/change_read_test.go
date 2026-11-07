package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
)

// seededForChangeReads polls the mockup estate and loads the dev seed.
func seededForChangeReads(t *testing.T) (*apiClient, func(user string) *apiClient) {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	return login(t, srv, "sam", testPassword), func(user string) *apiClient { return login(t, srv, user, testPassword) }
}

// viewerOnProd creates a user with only View on the Prod tag (which owns payments-api's cluster).
func viewerOnProd(t *testing.T) *apiClient {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(t.Context(), "viewer", hash, false, false)
	if err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListTags(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		if tag.Name == "Prod" {
			if err := st.SetPermission(t.Context(), user.ID, tag.ID, authz.Presets["Viewer"]); err != nil {
				t.Fatal(err)
			}
		}
	}
	return login(t, srv, "viewer", testPassword)
}

func TestGetTaskDefinition_ViewerGetsReadOnlyShape(t *testing.T) {
	c := viewerOnProd(t)
	resp := c.get("/api/services/" + paymentsID() + "/task-definition")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var out struct {
		Family           string         `json:"family"`
		DeployedRevision int            `json:"deployed_revision"`
		Definition       map[string]any `json:"definition"`
		Controller       struct {
			Type      string `json:"type"`
			Supported bool   `json:"supported"`
		} `json:"controller"`
		Strategy string          `json:"strategy"`
		Can      map[string]bool `json:"can"`
		Tag      TagDTO          `json:"tag"`
	}
	decode(t, resp, &out)
	if out.Family != "payments-api" || out.DeployedRevision != 42 || out.Definition["family"] != "payments-api" {
		t.Errorf("family %q revision %d definition family %v", out.Family, out.DeployedRevision, out.Definition["family"])
	}
	if _, has := out.Definition["taskDefinitionArn"]; has {
		t.Error("definition must be register-ready (no taskDefinitionArn)")
	}
	if out.Can["edit"] || out.Can["deploy"] || out.Can["operate"] || out.Can["admin"] {
		t.Errorf("can = %v, want read-only", out.Can)
	}
	if !out.Controller.Supported || out.Controller.Type != "ECS" || out.Tag.Name != "Prod" || out.Strategy == "" {
		t.Errorf("controller %+v tag %+v strategy %q", out.Controller, out.Tag, out.Strategy)
	}
}

func TestGetTaskDefinition_NoViewIs404(t *testing.T) {
	_, as := seededForChangeReads(t)
	// omer has no grants on Prod, which owns payments-api's cluster, so the service is invisible to him.
	c := as("omer")
	for _, path := range []string{"task-definition", "revisions", "image-tags", "desired-count"} {
		resp := c.get("/api/services/" + paymentsID() + "/" + path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

type revisionsBody struct {
	Items []struct {
		Revision     int    `json:"revision"`
		Status       string `json:"status"`
		RegisteredBy string `json:"registered_by"`
		ImageTag     string `json:"image_tag"`
	} `json:"items"`
	DeployedRevision   int  `json:"deployed_revision"`
	RollingOutRevision *int `json:"rolling_out_revision"`
}

func TestGetRevisions_Shape(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	sam, err := st.GetUserByUsername(t.Context(), "sam")
	if err != nil {
		t.Fatal(err)
	}
	// Register revision 43 as sam so registered_by comes from revision_records.
	svc := &deploy.Service{Store: st, Clients: fk, MaxDesiredCount: 50, Now: time.Now}
	paymentsKey := model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: "payments-api"}
	deployed, err := svc.DeployedDefinition(t.Context(), paymentsKey)
	if err != nil {
		t.Fatal(err)
	}
	draft := deploy.Definition{}
	for k, v := range deployed.Definition {
		draft[k] = v
	}
	containers := deployed.Definition["containerDefinitions"].([]any)
	copied := make([]any, len(containers))
	for i, c := range containers {
		m := map[string]any{}
		for k, v := range c.(map[string]any) {
			m[k] = v
		}
		if m["name"] == "payments-api" {
			m["image"] = homelabAccountID + ".dkr.ecr.us-east-1.amazonaws.com/payments-api:1.18.3"
		}
		copied[i] = m
	}
	draft["containerDefinitions"] = copied
	subject := authz.Subject{UserID: sam.ID, Username: "sam", Admin: true}
	if _, err := svc.Register(t.Context(), subject, paymentsKey, deploy.RegisterInput{BaseRevision: 42, Definition: draft}); err != nil {
		t.Fatal(err)
	}

	admin := login(t, srv, "sam", testPassword)
	var out revisionsBody
	decode(t, admin.get("/api/services/"+paymentsID()+"/revisions"), &out)
	var got []string
	for _, item := range out.Items {
		got = append(got, fmt.Sprintf("%d %s %s", item.Revision, item.Status, item.RegisteredBy))
	}
	want := []string{"43 not_deployed sam", "42 deployed terraform", "41 previous terraform", "40 previous terraform"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("items = %v, want %v", got, want)
	}
	if out.Items[0].ImageTag != "1.18.3" || out.DeployedRevision != 42 || out.RollingOutRevision != nil {
		t.Errorf("first %+v deployed %d rolling out %v", out.Items[0], out.DeployedRevision, out.RollingOutRevision)
	}

	var rolling revisionsBody
	decode(t, admin.get("/api/services/"+serviceID(homelabAccountID, "us-east-1", "core-services", "notifications")+"/revisions"), &rolling)
	if rolling.DeployedRevision != 9 || rolling.RollingOutRevision == nil || *rolling.RollingOutRevision != 10 {
		t.Errorf("notifications deployed %d rolling out %v, want 9 and 10", rolling.DeployedRevision, rolling.RollingOutRevision)
	}
}

func TestGetRevisionDefinition_ViewRequired(t *testing.T) {
	c := viewerOnProd(t)
	resp := c.get("/api/services/" + paymentsID() + "/revisions/42")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("viewer status %d, want 200", resp.StatusCode)
	}
	var out struct {
		Revision     int            `json:"revision"`
		ARN          string         `json:"arn"`
		Definition   map[string]any `json:"definition"`
		RegisteredBy string         `json:"registered_by"`
	}
	decode(t, resp, &out)
	if out.Revision != 42 || !strings.HasSuffix(out.ARN, "payments-api:42") || out.Definition["family"] != "payments-api" || out.RegisteredBy == "" {
		t.Fatalf("revision definition = %+v", out)
	}
	if _, has := out.Definition["taskDefinitionArn"]; has {
		t.Error("definition must be register-ready (no taskDefinitionArn)")
	}

	missing := c.get("/api/services/" + paymentsID() + "/revisions/999")
	if missing.StatusCode != http.StatusNotFound {
		t.Errorf("missing revision status %d, want 404", missing.StatusCode)
	}
	missing.Body.Close()

	// omer has no grants on Prod, so the service is invisible to him and the revision with it.
	_, as := seededForChangeReads(t)
	hidden := as("omer").get("/api/services/" + paymentsID() + "/revisions/42")
	if hidden.StatusCode != http.StatusNotFound {
		t.Errorf("invisible service status %d, want 404", hidden.StatusCode)
	}
	hidden.Body.Close()
}

func TestGetTaskDefinition_ThrottledIs503(t *testing.T) {
	admin, fk := adminWithFake(t)
	fk.FailNext("ListTagsForResource", &smithy.GenericAPIError{Code: "ThrottlingException", Message: "Rate exceeded"})
	resp := admin.get("/api/services/" + paymentsID() + "/task-definition")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "throttled" {
		t.Errorf("code %q, want throttled", code)
	}
}

func TestGetTaskDefinition_AWSErrorIs502(t *testing.T) {
	admin, fk := adminWithFake(t)
	fk.FailNext("ListTagsForResource", &smithy.GenericAPIError{Code: "AccessDeniedException", Message: "not allowed"})
	resp := admin.get("/api/services/" + paymentsID() + "/task-definition")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status %d, want 502", resp.StatusCode)
	}
	if code, _ := errorCode(t, resp); code != "aws_error" {
		t.Errorf("code %q, want aws_error", code)
	}
}

// adminWithFake returns an admin client over a polled estate plus the fake for error injection.
func adminWithFake(t *testing.T) (*apiClient, *fake.Fake) {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	return login(t, srv, "sam", testPassword), fk
}

func TestGetImageTags_ECRAndFallback(t *testing.T) {
	admin, _ := seededForChangeReads(t)
	var ecr struct {
		Source     string `json:"source"`
		Repository string `json:"repository"`
		Current    string `json:"current"`
		Tags       []struct {
			Tag string `json:"tag"`
		} `json:"tags"`
	}
	decode(t, admin.get("/api/services/"+paymentsID()+"/image-tags"), &ecr)
	if ecr.Source != "ecr" || ecr.Repository != "payments-api" || ecr.Current == "" || len(ecr.Tags) == 0 {
		t.Errorf("ecr = %+v", ecr)
	}
}

func TestGetImageTags_NoneForOtherAccountImage(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	fk.Estate.Mu.Lock()
	for i := range fk.Estate.TaskDefinitions {
		for j := range fk.Estate.TaskDefinitions[i].Definition.ContainerDefinitions {
			fk.Estate.TaskDefinitions[i].Definition.ContainerDefinitions[j].Image = aws.String("public.ecr.aws/nginx/nginx:1.27")
		}
	}
	fk.Estate.Mu.Unlock()
	pollAll(t, st, fk)
	var out struct {
		Source  string `json:"source"`
		Current string `json:"current"`
		Tags    []any  `json:"tags"`
	}
	decode(t, login(t, srv, "sam", testPassword).get("/api/services/"+paymentsID()+"/image-tags"), &out)
	if out.Source != "none" || out.Current != "1.27" || out.Tags == nil || len(out.Tags) != 0 {
		t.Errorf("fallback = %+v", out)
	}
}

func TestGetDesiredCount_Shape(t *testing.T) {
	admin, _ := seededForChangeReads(t)
	var raw map[string]any
	decode(t, admin.get("/api/services/"+paymentsID()+"/desired-count"), &raw)
	if raw["desired"] != float64(3) || raw["running"] != float64(3) || raw["max"] != float64(50) {
		t.Errorf("desired-count = %v", raw)
	}
	// The poller's scaling snapshot (fake.mockupScaling: payments-api min 2, max 6) feeds the range.
	if v, _ := raw["auto_scaling"].(map[string]any); v["min"] != float64(2) || v["max"] != float64(6) || len(v) != 2 {
		t.Errorf("auto_scaling = %v, want {min:2 max:6}", raw["auto_scaling"])
	}

	// edge-nginx has no scalable target in the seed.
	raw = nil
	decode(t, admin.get("/api/services/"+edgeNginxID()+"/desired-count"), &raw)
	if v, has := raw["auto_scaling"]; !has || v != nil {
		t.Errorf("edge-nginx auto_scaling = %v (present %v), want null", v, has)
	}
}

func TestGetService_IncludesControllerAndStrategyText(t *testing.T) {
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	fk.Estate.Mu.Lock()
	for i := range fk.Estate.Services {
		svc := &fk.Estate.Services[i].Service
		if aws.ToString(svc.ServiceName) == "notifications" {
			svc.DeploymentController = &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeCodeDeploy}
		}
	}
	fk.Estate.Mu.Unlock()
	pollAll(t, st, fk)
	admin := login(t, srv, "sam", testPassword)

	got := getService(t, admin, paymentsID())
	if !got.Controller.Supported || got.Controller.Banner != "" || got.Controller.Type != "ECS" {
		t.Errorf("payments controller = %+v", got.Controller)
	}
	want := "Rolling update · min healthy 100% · max 200% · circuit breaker with rollback is on"
	if got.StrategyText != want {
		t.Errorf("strategy_text = %q, want %q", got.StrategyText, want)
	}
	notifications := getService(t, admin, serviceID(homelabAccountID, "us-east-1", "core-services", "notifications"))
	if notifications.Controller.Supported || notifications.Controller.Banner != "This service is deployed by CodeDeploy. Harbormaster shows it but can't deploy it yet." {
		t.Errorf("notifications controller = %+v", notifications.Controller)
	}
}

func TestGetImageTags_QueryParams(t *testing.T) {
	admin, _ := seededForChangeReads(t)
	type imageTagsBody struct {
		Source    string `json:"source"`
		Container string `json:"container"`
		Image     string `json:"image"`
		Total     int    `json:"total"`
		Matched   int    `json:"matched"`
		Truncated bool   `json:"truncated"`
		Tags      []struct {
			Tag string `json:"tag"`
		} `json:"tags"`
	}
	var limited imageTagsBody
	decode(t, admin.get("/api/services/"+paymentsID()+"/image-tags?container=payments-api&limit=2"), &limited)
	if limited.Source != "ecr" || limited.Container != "payments-api" || !strings.HasSuffix(limited.Image, ".amazonaws.com/payments-api") {
		t.Errorf("limited = %+v", limited)
	}
	if len(limited.Tags) != 2 || limited.Total < 5 || limited.Matched != limited.Total || !limited.Truncated {
		t.Errorf("limit=2 -> %d tags, total %d, matched %d, truncated %v", len(limited.Tags), limited.Total, limited.Matched, limited.Truncated)
	}

	var searched imageTagsBody
	decode(t, admin.get("/api/services/"+paymentsID()+"/image-tags?q=1.18.0"), &searched)
	if searched.Matched != 1 || len(searched.Tags) != 1 || searched.Tags[0].Tag != "1.18.0" || searched.Truncated || searched.Total < 5 {
		t.Errorf("q=1.18.0 -> %+v", searched)
	}

	unknown := admin.get("/api/services/" + paymentsID() + "/image-tags?container=nope")
	if code, _ := errorCode(t, unknown); code != "invalid" || unknown.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown container -> %d %q, want 422 invalid", unknown.StatusCode, code)
	}
}

func TestGetImageTags_BadLimit422(t *testing.T) {
	admin, _ := seededForChangeReads(t)
	for _, limit := range []string{"0", "-1", "201", "abc"} {
		resp := admin.get("/api/services/" + paymentsID() + "/image-tags?limit=" + limit)
		if code, _ := errorCode(t, resp); code != "invalid" || resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("limit=%s -> %d %q, want 422 invalid", limit, resp.StatusCode, code)
		}
	}
}
