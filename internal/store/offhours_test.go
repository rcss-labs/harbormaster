package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func allAwake() string { return strings.Repeat("1", 168) }

func newSchedule(name, key, value string) store.OffHoursSchedule {
	return store.OffHoursSchedule{Name: name, Enabled: true, Selectors: awsSelectors(key, value), Timezone: "Europe/Berlin", Awake: allAwake()}
}

func awsSelectors(key, value string) []store.OffHoursSelector {
	return []store.OffHoursSelector{{Kind: "aws", Key: key, Value: value}}
}

func matchedServices(t *testing.T, s *store.Store, selectors ...store.OffHoursSelector) []string {
	t.Helper()
	got, err := s.OffHoursMatches(context.Background(), selectors)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, m := range got {
		names = append(names, m.Key.Service)
	}
	return names
}

// seedTaggedService stores an ACTIVE service whose AWS tags are written straight into raw.
func seedTaggedService(t *testing.T, s *store.Store, name, status, tagsJSON, extraRaw, taskDefARN string) model.ServiceKey {
	t.Helper()
	ctx := context.Background()
	key := serviceKey("core-services", name)
	raw := `{"Tags":` + tagsJSON + extraRaw + `}`
	_, err := s.UpsertServices(ctx, []model.Service{{Key: key, Status: status, Desired: 3, TaskDefinitionARN: taskDefARN,
		LaunchType: "FARGATE", Raw: []byte(raw), PolledAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestOffHoursSchedule_CRUDAndVersion(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	user, err := s.CreateUser(ctx, "ops", "hash", true, false)
	if err != nil {
		t.Fatal(err)
	}

	created, err := s.CreateOffHoursSchedule(ctx, newSchedule("staging nights", "env", "staging"), user.ID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.ID == 0 || created.Version != 1 || created.UpdatedBy == nil || *created.UpdatedBy != user.ID {
		t.Fatalf("created = %+v, want id set, version 1, updated by the user", created)
	}

	edit := created
	edit.Enabled = false
	updated, err := s.UpdateOffHoursSchedule(ctx, edit, user.ID)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 || updated.Enabled {
		t.Fatalf("updated = %+v, want version 2 and disabled", updated)
	}

	if _, err := s.UpdateOffHoursSchedule(ctx, edit, user.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update with stale version = %v, want ErrConflict", err)
	}

	got, err := s.GetOffHoursSchedule(ctx, created.ID)
	if err != nil || got.Version != 2 || got.Awake != allAwake() {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	list, err := s.ListOffHoursSchedules(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := s.DeleteOffHoursSchedule(ctx, created.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetOffHoursSchedule(ctx, created.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestOffHoursSchedule_DuplicateNameConflictsButSelectorsMayRepeat(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("one", "env", "staging"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("one", "env", "dev"), 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same name = %v, want ErrConflict", err)
	}
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("two", "env", "staging"), 0); err != nil {
		t.Fatalf("same selector on another schedule = %v, want it allowed", err)
	}
}

func TestOffHoursSchedule_RoundTripsSelectorsAndExclusions(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	sc := newSchedule("mixed", "env", "staging")
	sc.Selectors = append(sc.Selectors, store.OffHoursSelector{Kind: "harbormaster", TagID: qa.ID})
	sc.Exclusions = []model.ServiceKey{serviceKey("core-services", "db")}
	created, err := s.CreateOffHoursSchedule(ctx, sc, 0)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !reflect.DeepEqual(created.Selectors, sc.Selectors) || !reflect.DeepEqual(created.Exclusions, sc.Exclusions) {
		t.Fatalf("created = %+v, want the selectors and exclusions back", created)
	}
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("plain", "team", "web"), 0); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListOffHoursSchedules(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	byName := map[string]store.OffHoursSchedule{list[0].Name: list[0], list[1].Name: list[1]}
	if !reflect.DeepEqual(byName["mixed"].Selectors, sc.Selectors) || !reflect.DeepEqual(byName["mixed"].Exclusions, sc.Exclusions) {
		t.Fatalf("listed mixed = %+v", byName["mixed"])
	}
	if len(byName["plain"].Exclusions) != 0 || len(byName["plain"].Selectors) != 1 {
		t.Fatalf("listed plain = %+v", byName["plain"])
	}

	edit := created
	edit.Selectors = awsSelectors("env", "dev")
	edit.Exclusions = nil
	updated, err := s.UpdateOffHoursSchedule(ctx, edit, 0)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !reflect.DeepEqual(updated.Selectors, edit.Selectors) || len(updated.Exclusions) != 0 {
		t.Fatalf("updated = %+v, want the children replaced", updated)
	}
	// A stale version must not touch the children.
	stale := created
	stale.Selectors = awsSelectors("x", "y")
	if _, err := s.UpdateOffHoursSchedule(ctx, stale, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale update = %v, want ErrConflict", err)
	}
	got, err := s.GetOffHoursSchedule(ctx, created.ID)
	if err != nil || !reflect.DeepEqual(got.Selectors, edit.Selectors) {
		t.Fatalf("after stale update = %+v err=%v", got, err)
	}
}

func TestDeleteTag_RemovesItsSelectors(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	sc := newSchedule("mixed", "env", "staging")
	sc.Selectors = append(sc.Selectors, store.OffHoursSelector{Kind: "harbormaster", TagID: qa.ID})
	created, err := s.CreateOffHoursSchedule(ctx, sc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTag(ctx, qa.ID); err != nil {
		t.Fatalf("delete tag: %v", err)
	}
	got, err := s.GetOffHoursSchedule(ctx, created.ID)
	if err != nil || !reflect.DeepEqual(got.Selectors, awsSelectors("env", "staging")) {
		t.Fatalf("selectors after tag delete = %+v err=%v, want only the AWS one", got.Selectors, err)
	}
}

func TestOffHoursMatches_OrAcrossKindsWithoutDuplicates(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	both := seedTaggedService(t, s, "both", "ACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "aws-only", "ACTIVE", `[{"Key":"team","Value":"web"}]`, "", "")
	seedTaggedService(t, s, "hm-only", "ACTIVE", `[]`, "", "")
	seedTaggedService(t, s, "neither", "ACTIVE", `[]`, "", "")
	seedTaggedService(t, s, "inactive", "INACTIVE", `[{"Key":"team","Value":"web"}]`, "", "")
	if err := s.SetServiceTag(ctx, both, &qa.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetServiceTag(ctx, serviceKey("core-services", "hm-only"), &qa.ID); err != nil {
		t.Fatal(err)
	}

	got := matchedServices(t, s,
		store.OffHoursSelector{Kind: "aws", Key: "env", Value: "staging"},
		store.OffHoursSelector{Kind: "aws", Key: "team", Value: "web"},
		store.OffHoursSelector{Kind: "harbormaster", TagID: qa.ID})
	want := []string{"aws-only", "both", "hm-only"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matches = %v, want %v (each service once, ordered)", got, want)
	}
	if none := matchedServices(t, s); len(none) != 0 {
		t.Fatalf("no selectors matched %v, want nothing", none)
	}
}

func TestOffHoursMatches_HarbormasterTagUsesEffectiveTag(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	seedTaggedService(t, s, "inherits", "ACTIVE", `[]`, "", "")
	seedTaggedService(t, s, "direct", "ACTIVE", `[]`, "", "")
	if err := s.SetClusterTag(ctx, clusterKey("core-services"), qa.ID); err != nil {
		t.Fatal(err)
	}
	dev, err := s.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetServiceTag(ctx, serviceKey("core-services", "direct"), &dev.ID); err != nil {
		t.Fatal(err)
	}
	got := matchedServices(t, s, store.OffHoursSelector{Kind: "harbormaster", TagID: qa.ID})
	if !reflect.DeepEqual(got, []string{"inherits"}) {
		t.Fatalf("QA matches = %v, want only the service inheriting QA from its cluster", got)
	}
}

func TestSplitExcluded(t *testing.T) {
	a, b, c := serviceKey("c", "a"), serviceKey("c", "b"), serviceKey("c", "c")
	matches := []store.OffHoursMatch{{Key: a}, {Key: b}, {Key: c}}
	kept, excluded := store.SplitExcluded(matches, []model.ServiceKey{b, serviceKey("c", "gone")})
	if len(kept) != 2 || kept[0].Key != a || kept[1].Key != c || len(excluded) != 1 || excluded[0].Key != b {
		t.Fatalf("kept = %+v excluded = %+v", kept, excluded)
	}
	if kept, excluded := store.SplitExcluded(nil, nil); len(kept) != 0 || len(excluded) != 0 {
		t.Fatalf("empty split = %v %v", kept, excluded)
	}
}

func TestOffHoursHarbormasterTags_CountsActiveServices(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	qa, err := s.CreateTag(ctx, "QA", "blue")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTag(ctx, "Empty", "green"); err != nil {
		t.Fatal(err)
	}
	seedTaggedService(t, s, "one", "ACTIVE", `[]`, "", "")
	seedTaggedService(t, s, "two", "ACTIVE", `[]`, "", "")
	seedTaggedService(t, s, "gone", "INACTIVE", `[]`, "", "")
	seedTaggedService(t, s, "dev", "ACTIVE", `[]`, "", "")
	for _, name := range []string{"one", "two", "gone"} {
		if err := s.SetServiceTag(ctx, serviceKey("core-services", name), &qa.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.OffHoursHarbormasterTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, tag := range got {
		counts[tag.Name] = tag.Services
	}
	if counts["QA"] != 2 || counts["Empty"] != 0 || counts["Dev"] != 1 || len(got) != 3 {
		t.Fatalf("counts = %v (%+v), want QA 2, Empty 0, Dev 1", counts, got)
	}
}

// Migration 0180 turns the single tag_key/tag_value of an existing schedule into one AWS selector.
func TestMigrate_OffHoursSelectorsCarryExistingSchedules(t *testing.T) {
	s, ctx := storetest.OpenEmpty(t), context.Background()
	files, err := filepath.Glob("migrations/*.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	if _, err := s.Pool.Exec(ctx, `create table schema_migrations (version int primary key, applied_at timestamptz not null default now())`); err != nil {
		t.Fatal(err)
	}
	apply := func(file string) {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Pool.Exec(ctx, string(body)); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
		version, _ := strconv.Atoi(strings.SplitN(filepath.Base(file), "_", 2)[0])
		if _, err := s.Pool.Exec(ctx, `insert into schema_migrations (version) values ($1)`, version); err != nil {
			t.Fatal(err)
		}
	}
	const target = "migrations/0180_offhours_selectors.up.sql"
	for _, f := range files {
		if f < target {
			apply(f)
		}
	}
	if _, err := s.Pool.Exec(ctx, `insert into offhours_schedules (name, tag_key, tag_value, timezone, awake)
		values ('legacy', 'env', 'staging', 'UTC', $1), ('flag', 'flag', '', 'UTC', $1)`, allAwake()); err != nil {
		t.Fatal(err)
	}
	apply(target)

	list, err := s.ListOffHoursSchedules(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	byName := map[string]store.OffHoursSchedule{list[0].Name: list[0], list[1].Name: list[1]}
	if !reflect.DeepEqual(byName["legacy"].Selectors, awsSelectors("env", "staging")) ||
		!reflect.DeepEqual(byName["flag"].Selectors, awsSelectors("flag", "")) {
		t.Fatalf("carried selectors = %+v", byName)
	}
}

func TestOffHoursMatches_ExactTagOnly(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	want := seedTaggedService(t, s, "match", "ACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "longer-value", "ACTIVE", `[{"Key":"env","Value":"staging-old"}]`, "", "")
	seedTaggedService(t, s, "other-key-case", "ACTIVE", `[{"Key":"Env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "inactive", "INACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "untagged", "ACTIVE", `[]`, "", "")

	got, err := s.OffHoursMatches(ctx, awsSelectors("env", "staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Key != want || got[0].Desired != 3 {
		t.Fatalf("matches = %+v, want only %v", got, want)
	}
	if got[0].AWSTags["env"] != "staging" {
		t.Fatalf("AWSTags = %v", got[0].AWSTags)
	}
	dev, err := s.DevTag(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].EffectiveTagID != dev.ID {
		t.Fatalf("effective tag = %d, want the default %d", got[0].EffectiveTagID, dev.ID)
	}
}

func TestOffHoursMatches_EmptyValueMatchesNullAndEmpty(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedTaggedService(t, s, "null-value", "ACTIVE", `[{"Key":"flag","Value":null}]`, "", "")
	seedTaggedService(t, s, "empty-value", "ACTIVE", `[{"Key":"flag","Value":""}]`, "", "")
	seedTaggedService(t, s, "set-value", "ACTIVE", `[{"Key":"flag","Value":"on"}]`, "", "")

	got, err := s.OffHoursMatches(ctx, awsSelectors("flag", ""))
	if err != nil || len(got) != 2 || got[0].Key.Service != "empty-value" || got[1].Key.Service != "null-value" {
		t.Fatalf("matches for empty value = %+v err=%v, want empty-value and null-value", got, err)
	}
	if got, err := s.OffHoursMatches(ctx, awsSelectors("flag", "on")); err != nil || len(got) != 1 {
		t.Fatalf("matches for on = %+v err=%v, want only set-value", got, err)
	}
}

func TestOffHoursMatches_ReturnsPricingFacts(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	arn := "arn:aws:ecs:us-east-1:123456789012:task-definition/web:7"
	err := s.InTx(ctx, func(tx store.Tx) error {
		return tx.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{AccountID: serviceKey("core-services", "x").AccountID,
			Region: serviceKey("core-services", "x").Region, ARN: arn, Family: "web", Revision: 7, CPU: "512", Memory: "1024",
			Raw: []byte(`{"RuntimePlatform":{"CpuArchitecture":"ARM64"}}`), PolledAt: time.Now()})
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTaggedService(t, s, "web", "ACTIVE", `[{"Key":"env","Value":"staging"}]`,
		`,"CapacityProviderStrategy":[{"CapacityProvider":"FARGATE_SPOT","Weight":1}]`, arn)

	got, err := s.OffHoursMatches(ctx, awsSelectors("env", "staging"))
	if err != nil || len(got) != 1 {
		t.Fatalf("matches = %+v err=%v", got, err)
	}
	m := got[0]
	if m.CPU != "512" || m.Memory != "1024" || m.Architecture != "ARM64" || m.CapacityProvider != "FARGATE_SPOT" || m.LaunchType != "FARGATE" {
		t.Fatalf("pricing facts = %+v", m)
	}
}

func TestOffHoursTagValues_CountsServices(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	seedTaggedService(t, s, "a", "ACTIVE", `[{"Key":"env","Value":"staging"},{"Key":"team","Value":"x"}]`, "", "")
	seedTaggedService(t, s, "b", "ACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "c", "ACTIVE", `[{"Key":"env","Value":"dev"}]`, "", "")
	seedTaggedService(t, s, "gone", "INACTIVE", `[{"Key":"env","Value":"dev"}]`, "", "")
	// The SDK marshals an unset tag value as null; it counts as the empty value.
	seedTaggedService(t, s, "null-value", "ACTIVE", `[{"Key":"x","Value":null},{"Key":"x","Value":""}]`, "", "")

	got, err := s.OffHoursTagValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[[2]string]int{}
	for _, v := range got {
		counts[[2]string{v.Key, v.Value}] = v.Services
	}
	want := map[[2]string]int{{"env", "staging"}: 2, {"env", "dev"}: 1, {"team", "x"}: 1, {"x", ""}: 1}
	if len(counts) != len(want) {
		t.Fatalf("tag values = %v, want %v", counts, want)
	}
	for k, n := range want {
		if counts[k] != n {
			t.Fatalf("tag values = %v, want %v", counts, want)
		}
	}
}

func enrolmentFor(key model.ServiceKey, scheduleID *int64) store.OffHoursEnrolment {
	desired := 3
	return store.OffHoursEnrolment{Key: key, ScheduleID: scheduleID, SavedMin: 1, SavedMax: 6, SavedDesired: &desired,
		RegisteredTarget: true, AwakeTasks: 2, State: "active"}
}

func TestOffHoursEnrolment_UpsertGetListDelete(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	sc, err := s.CreateOffHoursSchedule(ctx, newSchedule("n", "env", "staging"), 0)
	if err != nil {
		t.Fatal(err)
	}
	key := serviceKey("core-services", "web")
	e := enrolmentFor(key, &sc.ID)
	if err := s.InTx(ctx, func(tx store.Tx) error { return s.UpsertOffHoursEnrolment(ctx, tx, e) }); err != nil {
		t.Fatal(err)
	}
	e.State, e.BlockedReason = "blocked", "scaling policy"
	if err := s.InTx(ctx, func(tx store.Tx) error { return s.UpsertOffHoursEnrolment(ctx, tx, e) }); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOffHoursEnrolment(ctx, key)
	if err != nil || got.State != "blocked" || got.BlockedReason != "scaling policy" || got.SavedMax != 6 ||
		got.SavedDesired == nil || *got.SavedDesired != 3 || got.ScheduleID == nil || *got.ScheduleID != sc.ID {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if list, err := s.ListOffHoursEnrolments(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := s.InTx(ctx, func(tx store.Tx) error { return s.DeleteOffHoursEnrolment(ctx, tx, key) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetOffHoursEnrolment(ctx, key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestOffHoursEnrolment_DeleteScheduleLeavesRowLeaving(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	sc, err := s.CreateOffHoursSchedule(ctx, newSchedule("n", "env", "staging"), 0)
	if err != nil {
		t.Fatal(err)
	}
	key := serviceKey("core-services", "web")
	if err := s.InTx(ctx, func(tx store.Tx) error { return s.UpsertOffHoursEnrolment(ctx, tx, enrolmentFor(key, &sc.ID)) }); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOffHoursSchedule(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOffHoursEnrolment(ctx, key)
	if err != nil {
		t.Fatalf("enrolment must survive schedule delete: %v", err)
	}
	if got.ScheduleID != nil || got.SavedMax != 6 {
		t.Fatalf("enrolment = %+v, want nil schedule and saved capacity kept", got)
	}
}

func TestOffHoursEnrolment_LockBlocksSecondTx(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	key := serviceKey("core-services", "web")
	if err := s.InTx(ctx, func(tx store.Tx) error { return s.UpsertOffHoursEnrolment(ctx, tx, enrolmentFor(key, nil)) }); err != nil {
		t.Fatal(err)
	}

	locked, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- s.InTx(ctx, func(tx store.Tx) error {
			if _, err := s.LockOffHoursEnrolment(ctx, tx, key); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	err := s.InTx(short, func(tx store.Tx) error {
		_, err := s.LockOffHoursEnrolment(short, tx, key)
		return err
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock = %v, want it to wait until the context deadline", err)
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := s.InTx(ctx, func(tx store.Tx) error {
		_, err := s.LockOffHoursEnrolment(ctx, tx, key)
		return err
	}); err != nil {
		t.Fatalf("lock after the first commit: %v", err)
	}
}

func TestMigrate_AwakeCheckRejectsBadGrid(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	for name, awake := range map[string]string{
		"too short": strings.Repeat("1", 167), "too long": strings.Repeat("0", 169),
		"bad digit": strings.Repeat("1", 167) + "2", "empty": "",
	} {
		sc := newSchedule("bad "+name, "env", name)
		sc.Awake = awake
		if _, err := s.CreateOffHoursSchedule(ctx, sc, 0); err == nil {
			t.Errorf("%s: grid accepted, want a check violation", name)
		}
	}
}

// A tag-only change must reach services.raw (the off-hours reconciler matches on it); the
// same tags must not rewrite the row.
func TestUpsertServices_TagOnlyChangeUpdatesRaw(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	key := serviceKey("core-services", "api")
	svc := func(tags map[string]string, rawTags string) model.Service {
		return model.Service{Key: key, Status: "ACTIVE", Desired: 2, Tags: tags,
			Raw: []byte(`{"Tags":` + rawTags + `}`), PolledAt: time.Now()}
	}
	staging := `[{"Key":"env","Value":"staging"}]`
	steps := []struct {
		name string
		row  model.Service
		want store.WriteResult
	}{
		{"insert", svc(map[string]string{"env": "staging"}, staging), store.WriteResult{Inserted: 1}},
		{"identical", svc(map[string]string{"env": "staging"}, staging), store.WriteResult{Unchanged: 1}},
		{"tag edited", svc(map[string]string{"env": "prod"}, `[{"Key":"env","Value":"prod"}]`), store.WriteResult{Updated: 1}},
		{"tag removed", svc(nil, `[]`), store.WriteResult{Updated: 1}},
		{"tag added", svc(map[string]string{"env": "staging"}, staging), store.WriteResult{Updated: 1}},
	}
	for _, step := range steps {
		got, err := s.UpsertServices(ctx, []model.Service{step.row})
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got != step.want {
			t.Fatalf("%s: write result = %+v, want %+v", step.name, got, step.want)
		}
		var rawTags string
		if err := s.Pool.QueryRow(ctx,
			`select coalesce(raw -> 'Tags', 'null')::text from services where cluster_name = $1 and service_name = $2`,
			key.Cluster, key.Service).Scan(&rawTags); err != nil {
			t.Fatal(err)
		}
		var want, have any
		_ = json.Unmarshal(step.row.Raw, &want)
		_ = json.Unmarshal([]byte(rawTags), &have)
		if !reflect.DeepEqual(want.(map[string]any)["Tags"], have) {
			t.Fatalf("%s: stored raw Tags = %s, want %s", step.name, rawTags, step.row.Raw)
		}
	}
}
