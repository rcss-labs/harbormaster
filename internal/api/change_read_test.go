package api

import (
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
)

// seededForChangeReads polls the mockup estate and loads the dev seed.
func seededForChangeReads(t *testing.T) (*apiClient, func(user string) *apiClient) {
	t.Helper()
	srv, fk, st := newTestServer(t)
	seedAccounts(t, st)
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	return login(t, srv, "pavel", testPassword), func(user string) *apiClient { return login(t, srv, user, testPassword) }
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
	// oncall has no permission on Dev only; payments-api is Prod. Use a user with no grants at all.
	c := as("omer")
	for _, path := range []string{"task-definition", "revisions", "image-tags", "desired-count"} {
		resp := c.get("/api/services/" + paymentsID() + "/" + path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestGetRevisions_Shape(t *testing.T) {
	admin, _ := seededForChangeReads(t)
	var out struct {
		Items []struct {
			Revision     int    `json:"revision"`
			Status       string `json:"status"`
			RegisteredBy string `json:"registered_by"`
			ImageTag     string `json:"image_tag"`
		} `json:"items"`
		DeployedRevision   int  `json:"deployed_revision"`
		RollingOutRevision *int `json:"rolling_out_revision"`
	}
	decode(t, admin.get("/api/services/"+paymentsID()+"/revisions"), &out)
	if out.DeployedRevision != 42 || out.RollingOutRevision != nil {
		t.Errorf("deployed %d rolling out %v", out.DeployedRevision, out.RollingOutRevision)
	}
	if len(out.Items) != 3 || out.Items[0].Revision != 42 || out.Items[0].Status != "deployed" || out.Items[0].RegisteredBy != "terraform" || out.Items[0].ImageTag == "" {
		t.Errorf("items = %+v", out.Items)
	}
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
	decode(t, login(t, srv, "pavel", testPassword).get("/api/services/"+paymentsID()+"/image-tags"), &out)
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
	if v, has := raw["auto_scaling"]; !has || v != nil {
		t.Errorf("auto_scaling = %v (present %v), want null", v, has)
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
	admin := login(t, srv, "pavel", testPassword)

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
