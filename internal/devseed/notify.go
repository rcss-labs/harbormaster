package devseed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Option adjusts what Apply seeds beyond the shared mockup data.
type Option func(*options)

type options struct {
	notifyBox     *secretbox.Box
	notifyBaseURL string
	notifyDemo    bool
	agentDemo     bool
	reviewBox     *secretbox.Box
	reviewBaseURL string
}

// WithNotifyDemo seeds the demo notification routes, sealed with box and posting to the in-process receiver under
// baseURL. It seeds nothing when box is nil (no HM_SESSION_KEY) or when any route already exists, so a restart never
// touches routes an admin made.
func WithNotifyDemo(box *secretbox.Box, baseURL string) Option {
	return func(o *options) {
		o.notifyBox, o.notifyBaseURL, o.notifyDemo = box, strings.TrimRight(baseURL, "/"), true
	}
}

// Demo credentials: shaped like the real ones so validation accepts them, and worthless.
const (
	demoPagerDutyKey = "demo00000000000000000000000000ab"
	demoTelegramBot  = "123456789:AAHdemo-token-for-fake-mode-only-0001"
)

// seedNotifications creates the six demo routes (a channel per cluster, service and account, a Telegram chat and
// a catch-all), unless routes exist already. Keys come from the mockup estate, never from literals.
func seedNotifications(ctx context.Context, st *store.Store, o options) error {
	if !o.notifyDemo || o.notifyBox == nil {
		return nil
	}
	existing, err := st.ListNotificationRoutes(ctx)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	estate := fake.MockupEstate(time.Now)
	sandbox, staging := accountIDByName(estate, "sandbox-admin"), accountIDByName(estate, "staging-example")
	allKinds := make([]string, len(notify.Kinds))
	for i, k := range notify.Kinds {
		allKinds[i] = k.Kind
	}
	hook := func(name string) notify.Secret {
		return notify.Secret{URL: o.notifyBaseURL + "/fake-notify/slack/" + name}
	}

	demos := []struct {
		route  store.NotificationRoute
		secret *notify.Secret
	}{
		{store.NotificationRoute{
			Name: "Prod incidents to on-call", Enabled: true, Destination: notify.DestPagerDuty,
			Config:      store.NotificationConfig{Label: "On-call", PagerDutyRegion: "us"},
			MinSeverity: "critical", Kinds: allKinds, RepeatSeconds: 1800,
		}, &notify.Secret{RoutingKey: demoPagerDutyKey}},
		{store.NotificationRoute{
			Name: "core-services to #core-alerts", Enabled: true, Destination: notify.DestSlack,
			Config:      store.NotificationConfig{Label: "#core-alerts", URLHost: hostOf(o.notifyBaseURL)},
			MinSeverity: "warning", Kinds: allKinds, QuietEnabled: true,
			Scope: []store.NotificationSelector{{Kind: store.NotificationSelectorCluster, AccountID: sandbox, Region: "us-east-1", Cluster: "core-services", Threshold: 1}},
		}, ptr(hook("core-alerts"))},
		{store.NotificationRoute{
			Name: "image-resizer to Telegram", Enabled: true, Destination: notify.DestTelegram,
			Config:      store.NotificationConfig{ChatID: "-1001234567890"},
			MinSeverity: "warning", Kinds: allKinds,
			Scope: []store.NotificationSelector{{Kind: store.NotificationSelectorService, AccountID: sandbox, Region: "us-east-1", Cluster: "background-jobs", Service: "image-resizer"}},
		}, &notify.Secret{BotToken: demoTelegramBot}},
		{store.NotificationRoute{
			Name: "Staging account by email", Enabled: false, Destination: notify.DestEmail,
			Config:      store.NotificationConfig{Recipients: []string{"platform@example.com"}},
			MinSeverity: "warning", Kinds: allKinds,
			Scope: []store.NotificationSelector{{Kind: store.NotificationSelectorAccount, AccountID: staging, Threshold: 2}},
		}, nil},
		{store.NotificationRoute{
			Name: "Half of core-services down", Enabled: true, Destination: notify.DestPagerDuty,
			Config:      store.NotificationConfig{Label: "On-call", PagerDutyRegion: "us"},
			MinSeverity: "critical", Kinds: allKinds,
			Scope: []store.NotificationSelector{{Kind: store.NotificationSelectorCluster, AccountID: sandbox, Region: "us-east-1", Cluster: "core-services", Threshold: 2}},
		}, &notify.Secret{RoutingKey: demoPagerDutyKey}},
		{store.NotificationRoute{
			Name: "Everything else", Enabled: true, Destination: notify.DestSlack, Fallback: true,
			Config:      store.NotificationConfig{Label: "#catch-all", URLHost: hostOf(o.notifyBaseURL)},
			MinSeverity: "warning", Kinds: allKinds,
		}, ptr(hook("catch-all"))},
	}
	for _, demo := range demos {
		demo.route.QuietTimezone, demo.route.NotifyResolved = "Europe/Berlin", true
		if demo.secret != nil {
			raw, err := json.Marshal(demo.secret)
			if err != nil {
				return err
			}
			if demo.route.SecretEnc, err = o.notifyBox.Seal(raw); err != nil {
				return err
			}
		}
		if _, err := st.CreateNotificationRoute(ctx, demo.route, 0); err != nil {
			return fmt.Errorf("seed notification route %s: %w", demo.route.Name, err)
		}
	}
	return nil
}

func ptr[T any](v T) *T { return &v }

// hostOf is the host part of a base URL, for the route's display target.
func hostOf(base string) string {
	base = strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
	host, _, _ := strings.Cut(base, "/")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}
