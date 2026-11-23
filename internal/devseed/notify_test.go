package devseed_test

import (
	"encoding/json"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/devseed"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
	"time"
)

func TestApply_SeedsDemoRoutesOnce(t *testing.T) {
	ctx := t.Context()
	box, err := secretbox.New("dev-session-key", secretbox.PurposeNotifySecret)
	if err != nil {
		t.Fatal(err)
	}
	estate := fake.MockupEstate(time.Now)
	accountID := func(name string) string {
		for _, a := range estate.Accounts {
			if a.Name == name {
				return a.ID
			}
		}
		t.Fatalf("no account %q in the estate", name)
		return ""
	}

	st := storetest.Open(t)
	for i := 0; i < 2; i++ {
		if err := devseed.Apply(ctx, st, true, devseed.WithNotifyDemo(box, "http://127.0.0.1:8480")); err != nil {
			t.Fatalf("apply #%d: %v", i+1, err)
		}
	}
	routes, err := st.ListNotificationRoutes(ctx)
	if err != nil || len(routes) != 6 {
		t.Fatalf("routes = %d err=%v, want 6 after two applies", len(routes), err)
	}
	byName := map[string]store.NotificationRoute{}
	for _, r := range routes {
		byName[r.Name] = r
	}

	prod := byName["Prod incidents to on-call"]
	if prod.Destination != "pagerduty" || prod.MinSeverity != "critical" || prod.RepeatSeconds != 1800 || !prod.Enabled || len(prod.Scope) != 0 {
		t.Errorf("prod route = %+v", prod)
	}
	core := byName["core-services to #core-alerts"]
	if core.Destination != "slack" || !core.Enabled || !core.QuietEnabled || len(core.Scope) != 1 ||
		core.Scope[0] != (store.NotificationSelector{Kind: "cluster", AccountID: accountID("sandbox-admin"), Region: "us-east-1", Cluster: "core-services", Threshold: 1}) {
		t.Errorf("core route = %+v", core)
	}
	telegram := byName["image-resizer to Telegram"]
	if telegram.Destination != "telegram" || telegram.Config.ChatID != "-1001234567890" || !telegram.Enabled || len(telegram.Scope) != 1 ||
		telegram.Scope[0] != (store.NotificationSelector{Kind: "service", AccountID: accountID("sandbox-admin"), Region: "us-east-1", Cluster: "background-jobs", Service: "image-resizer"}) {
		t.Errorf("telegram route = %+v", telegram)
	}
	email := byName["Staging account by email"]
	if email.Destination != "email" || email.Enabled || len(email.Config.Recipients) != 1 || email.Config.Recipients[0] != "platform@example.com" || len(email.Scope) != 1 ||
		email.Scope[0] != (store.NotificationSelector{Kind: "account", AccountID: accountID("staging-example"), Threshold: 2}) {
		t.Errorf("email route = %+v", email)
	}
	half := byName["Half of core-services down"]
	if half.Destination != "pagerduty" || half.MinSeverity != "critical" || !half.Enabled || len(half.Scope) != 1 ||
		half.Scope[0] != (store.NotificationSelector{Kind: "cluster", AccountID: accountID("sandbox-admin"), Region: "us-east-1", Cluster: "core-services", Threshold: 2}) {
		t.Errorf("half route = %+v, want the same cluster as the first route with a threshold of 2", half)
	}
	catchAll := byName["Everything else"]
	if catchAll.Destination != "slack" || !catchAll.Fallback || !catchAll.Enabled || catchAll.MinSeverity != "warning" {
		t.Errorf("catch-all route = %+v", catchAll)
	}

	// The secrets open under the dev key and point at the in-process receiver.
	var secret notify.Secret
	plain, err := box.Open(core.SecretEnc)
	if err != nil || json.Unmarshal(plain, &secret) != nil || secret.URL != "http://127.0.0.1:8480/fake-notify/slack/core-alerts" {
		t.Errorf("core secret = %+v err=%v", secret, err)
	}
	plain, err = box.Open(catchAll.SecretEnc)
	if err != nil || json.Unmarshal(plain, &secret) != nil || secret.URL != "http://127.0.0.1:8480/fake-notify/slack/catch-all" {
		t.Errorf("catch-all secret = %+v err=%v", secret, err)
	}

	// Without the option nothing is seeded, and an existing route stops the seed.
	off := storetest.Open(t)
	if err := devseed.Apply(ctx, off, true); err != nil {
		t.Fatal(err)
	}
	if routes, _ := off.ListNotificationRoutes(ctx); len(routes) != 0 {
		t.Errorf("routes without the option = %d, want 0", len(routes))
	}
	mine := storetest.Open(t)
	if _, err := mine.CreateNotificationRoute(ctx, store.NotificationRoute{Name: "mine", Destination: "email", MinSeverity: "warning", Kinds: []string{"service_failing"}, QuietTimezone: "UTC"}, 0); err != nil {
		t.Fatal(err)
	}
	if err := devseed.Apply(ctx, mine, true, devseed.WithNotifyDemo(box, "http://127.0.0.1:8480")); err != nil {
		// An existing route is not a populated-database refusal (no users or accounts), so Apply must succeed.
		t.Fatal(err)
	}
	if routes, _ := mine.ListNotificationRoutes(ctx); len(routes) != 1 {
		t.Errorf("routes with an existing one = %d, want only the admin's", len(routes))
	}
	// A nil box seeds nothing (no HM_SESSION_KEY).
	noKey := storetest.Open(t)
	if err := devseed.Apply(ctx, noKey, true, devseed.WithNotifyDemo(nil, "http://127.0.0.1:8480")); err != nil {
		t.Fatal(err)
	}
	if routes, _ := noKey.ListNotificationRoutes(ctx); len(routes) != 0 {
		t.Errorf("routes without a session key = %d, want 0", len(routes))
	}
}
