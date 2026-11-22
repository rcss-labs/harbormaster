package store_test

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// A selector matches a single service exactly when OffHoursMatches lists that service for it.
func TestOffHoursSelectorMatches_AgreesWithOffHoursMatches(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedTaggedService(t, s, "null-value", "ACTIVE", `[{"Key":"flag","Value":null}]`, "", "")
	seedTaggedService(t, s, "empty-value", "ACTIVE", `[{"Key":"flag","Value":""}]`, "", "")
	seedTaggedService(t, s, "staging", "ACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "staging-old", "ACTIVE", `[{"Key":"env","Value":"staging-old"}]`, "", "")
	seedTaggedService(t, s, "wrong-case", "ACTIVE", `[{"Key":"Env","Value":"staging"}]`, "", "")
	untagged := seedTaggedService(t, s, "untagged", "ACTIVE", `[]`, "", "")
	inactive := seedTaggedService(t, s, "inactive", "INACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	dev, err := s.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	selectors := []store.OffHoursSelector{
		{Kind: "aws", Key: "flag", Value: ""}, {Kind: "aws", Key: "env", Value: "staging"},
		{Kind: "aws", Key: "flag", Value: "on"}, {Kind: "harbormaster", TagID: dev.ID},
	}
	all := []string{"null-value", "empty-value", "staging", "staging-old", "wrong-case", "untagged"}
	for _, sel := range selectors {
		listed := map[string]bool{}
		for _, name := range matchedServices(t, s, sel) {
			listed[name] = true
		}
		for _, name := range all {
			facts, ok, err := s.OffHoursServiceFacts(ctx, serviceKey("core-services", name))
			if err != nil || !ok {
				t.Fatalf("OffHoursServiceFacts(%s) ok=%v err=%v", name, ok, err)
			}
			if got := sel.Matches(facts); got != listed[name] {
				t.Errorf("selector %+v on %s: Matches=%v, OffHoursMatches lists it=%v", sel, name, got, listed[name])
			}
		}
	}
	_ = untagged
	if _, ok, err := s.OffHoursServiceFacts(ctx, inactive); err != nil || ok {
		t.Errorf("inactive service facts ok=%v err=%v, want not found", ok, err)
	}
}
