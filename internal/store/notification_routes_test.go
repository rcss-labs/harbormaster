package store_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func newRoute(name string) store.NotificationRoute {
	return store.NotificationRoute{
		Name: name, Destination: "webhook",
		Config:      store.NotificationConfig{URLHost: "hooks.example"},
		SecretEnc:   []byte("sealed"),
		MinSeverity: "warning", Kinds: []string{"service_failing", "restart_loop"},
		RepeatSeconds: 1800, QuietTimezone: "Europe/Berlin", NotifyResolved: true,
	}
}

func TestNotificationRoute_CRUDAndVersion(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	created, err := s.CreateNotificationRoute(ctx, newRoute("Prod"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Version != 1 || created.Enabled {
		t.Fatalf("created = %+v, want an id, version 1 and off", created)
	}
	got, err := s.GetNotificationRoute(ctx, created.ID)
	if err != nil || got.Name != "Prod" || string(got.SecretEnc) != "sealed" {
		t.Fatalf("get = %+v err=%v", got, err)
	}

	edit := got
	edit.Name, edit.Enabled, edit.MinSeverity = "Prod 2", true, "critical"
	updated, err := s.UpdateNotificationRoute(ctx, edit, 0)
	if err != nil || updated.Version != 2 || updated.Name != "Prod 2" || !updated.Enabled {
		t.Fatalf("update = %+v err=%v", updated, err)
	}
	if _, err := s.UpdateNotificationRoute(ctx, edit, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	list, err := s.ListNotificationRoutes(ctx)
	if err != nil || len(list) != 1 || list[0].Name != "Prod 2" {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	if err := s.DeleteNotificationRoute(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNotificationRoute(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteNotificationRoute(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestNotificationRoute_RoundTripsScopeConfigAndKinds(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	r := newRoute("Everything scoped")
	r.Destination = "telegram"
	r.Config = store.NotificationConfig{ChatID: "-1001234567890", ThreadID: 7, Label: "ops"}
	r.Fallback = true
	r.Scope = []store.NotificationSelector{
		{Kind: "account", AccountID: "111111111111", Threshold: 1},
		{Kind: "account", AccountID: "222222222222", Region: "eu-west-1", Threshold: 3},
		{Kind: "cluster", AccountID: "111111111111", Region: "us-east-1", Cluster: "core-services", Threshold: 2},
		{Kind: "service", AccountID: "111111111111", Region: "us-east-1", Cluster: "core-services", Service: "api"},
		{Kind: "aws", Key: "env", Value: "staging"},
		{Kind: "harbormaster", TagID: qa.ID},
	}
	created, err := s.CreateNotificationRoute(ctx, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetNotificationRoute(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Fallback || !reflect.DeepEqual(got.Config, r.Config) || !reflect.DeepEqual(got.Kinds, r.Kinds) {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if len(got.Scope) != len(r.Scope) {
		t.Fatalf("scope = %+v, want %d selectors", got.Scope, len(r.Scope))
	}
	want := map[store.NotificationSelector]bool{}
	for _, sel := range r.Scope {
		want[sel] = true
	}
	for _, sel := range got.Scope {
		if !want[sel] {
			t.Errorf("unexpected selector %+v", sel)
		}
	}

	// Replacing the scope on update replaces, not appends; an empty scope means everything.
	edit := got
	edit.Scope = nil
	updated, err := s.UpdateNotificationRoute(ctx, edit, 0)
	if err != nil || len(updated.Scope) != 0 {
		t.Fatalf("scope after clearing = %+v err=%v", updated.Scope, err)
	}
}

func TestNotificationRoute_DuplicateSelectorIsStoredOnce(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	r := newRoute("dupes")
	sel := store.NotificationSelector{Kind: "account", AccountID: "111111111111", Threshold: 1}
	r.Scope = []store.NotificationSelector{sel, sel}
	created, err := s.CreateNotificationRoute(ctx, r, 0)
	if err != nil || len(created.Scope) != 1 {
		t.Fatalf("scope = %+v err=%v, want one selector", created.Scope, err)
	}
}

func TestMigrate_NotificationScopeShapeIsChecked(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	created, err := s.CreateNotificationRoute(ctx, newRoute("shape"), 0)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	for name, sql := range map[string]string{
		"cluster without a region":     `insert into notification_route_scopes (route_id, kind, account_id, cluster_name) values ($1, 'cluster', '111111111111', 'c')`,
		"service with a tag id":        `insert into notification_route_scopes (route_id, kind, account_id, region, cluster_name, service_name, tag_id) values ($1, 'service', '111111111111', 'r', 'c', 's', ` + strconv.FormatInt(int64(tag.ID), 10) + `)`,
		"account carrying a cluster":   `insert into notification_route_scopes (route_id, kind, account_id, cluster_name) values ($1, 'account', '111111111111', 'c')`,
		"aws without a value":          `insert into notification_route_scopes (route_id, kind, aws_key) values ($1, 'aws', 'env')`,
		"unknown kind":                 `insert into notification_route_scopes (route_id, kind, account_id) values ($1, 'region', '111111111111')`,
		"harbormaster without tag id":  `insert into notification_route_scopes (route_id, kind) values ($1, 'harbormaster')`,
		"cluster without a threshold":  `insert into notification_route_scopes (route_id, kind, account_id, region, cluster_name) values ($1, 'cluster', '111111111111', 'r', 'c')`,
		"service with a threshold":     `insert into notification_route_scopes (route_id, kind, account_id, region, cluster_name, service_name, threshold) values ($1, 'service', '111111111111', 'r', 'c', 's', 2)`,
		"aws with a threshold":         `insert into notification_route_scopes (route_id, kind, aws_key, aws_value, threshold) values ($1, 'aws', 'env', 'x', 1)`,
		"threshold of zero":            `insert into notification_route_scopes (route_id, kind, account_id, threshold) values ($1, 'account', '111111111111', 0)`,
		"threshold of 501":             `insert into notification_route_scopes (route_id, kind, account_id, threshold) values ($1, 'account', '111111111111', 501)`,
		"harbormaster with an account": `insert into notification_route_scopes (route_id, kind, account_id, tag_id) values ($1, 'harbormaster', '111111111111', ` + strconv.FormatInt(int64(tag.ID), 10) + `)`,
	} {
		if _, err := s.Pool.Exec(ctx, sql, created.ID); err == nil {
			t.Errorf("%s: insert succeeded, want a check violation", name)
		}
	}
}

func TestNotificationRoute_DuplicateNameConflictsCaseInsensitively(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	if _, err := s.CreateNotificationRoute(ctx, newRoute("Prod"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateNotificationRoute(ctx, newRoute("PROD"), 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate create = %v, want ErrConflict", err)
	}
	other, err := s.CreateNotificationRoute(ctx, newRoute("Other"), 0)
	if err != nil {
		t.Fatal(err)
	}
	other.Name = "prod"
	if _, err := s.UpdateNotificationRoute(ctx, other, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("rename onto an existing name = %v, want ErrConflict", err)
	}
}

func TestNotificationRoute_LimitIs50(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	for i := 0; i < store.MaxNotificationRoutes; i++ {
		if _, err := s.CreateNotificationRoute(ctx, newRoute("route-"+strconv.Itoa(i)), 0); err != nil {
			t.Fatalf("route %d: %v", i, err)
		}
	}
	if _, err := s.CreateNotificationRoute(ctx, newRoute("one too many"), 0); !errors.Is(err, store.ErrRouteLimit) {
		t.Fatalf("51st route = %v, want ErrRouteLimit", err)
	}
}

// A deleted tag leaves its selector in the route, so the route can say the tag is gone and never widens to
// "everything" (D27).
func TestDeleteTag_KeepsNotificationSelectors(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	prod, err := s.CreateTag(ctx, "Prod", "red")
	if err != nil {
		t.Fatal(err)
	}
	r := newRoute("tagged")
	r.Scope = []store.NotificationSelector{{Kind: "aws", Key: "env", Value: "staging"}, {Kind: "harbormaster", TagID: qa.ID}, {Kind: "harbormaster", TagID: prod.ID}}
	created, err := s.CreateNotificationRoute(ctx, r, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []authz.TagID{qa.ID, prod.ID} { // two deleted tags on one route must not collide
		if err := s.DeleteTag(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetNotificationRoute(ctx, created.ID)
	if err != nil {
		t.Fatalf("the route must survive its tag: %v", err)
	}
	if len(got.Scope) != 3 {
		t.Fatalf("scope after tag delete = %+v, want all three selectors kept", got.Scope)
	}
}

func TestMigrate_NotificationChecksRejectBadValues(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	bad := map[string]func(*store.NotificationRoute){
		"repeat 60":           func(r *store.NotificationRoute) { r.RepeatSeconds = 60 },
		"no kinds":            func(r *store.NotificationRoute) { r.Kinds = nil },
		"unknown destination": func(r *store.NotificationRoute) { r.Destination = "pager" },
		"unknown severity":    func(r *store.NotificationRoute) { r.MinSeverity = "info" },
	}
	for name, mutate := range bad {
		r := newRoute("bad " + name)
		mutate(&r)
		if _, err := s.CreateNotificationRoute(ctx, r, 0); err == nil {
			t.Errorf("%s: create succeeded, want a check violation", name)
		}
	}
	r := newRoute("telegram ok")
	r.Destination = "telegram"
	if _, err := s.CreateNotificationRoute(ctx, r, 0); err != nil {
		t.Errorf("telegram must be a valid destination: %v", err)
	}
}

func TestNotificationRoute_AWSSelectorKeepsAnEmptyValue(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	r := newRoute("empty value")
	r.Scope = []store.NotificationSelector{{Kind: "aws", Key: "env"}}
	created, err := s.CreateNotificationRoute(ctx, r, 0)
	if err != nil || len(created.Scope) != 1 || created.Scope[0].Key != "env" || created.Scope[0].Value != "" {
		t.Fatalf("scope = %+v err=%v, want the env tag with an empty value", created.Scope, err)
	}
}
