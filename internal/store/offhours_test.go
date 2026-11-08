package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

func allAwake() string { return strings.Repeat("1", 168) }

func newSchedule(name, key, value string) store.OffHoursSchedule {
	return store.OffHoursSchedule{Name: name, Enabled: true, TagKey: key, TagValue: value, Timezone: "Europe/Berlin", Awake: allAwake()}
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

func TestOffHoursSchedule_DuplicateSelectorConflicts(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("one", "env", "staging"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("two", "env", "staging"), 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same selector = %v, want ErrConflict", err)
	}
	if _, err := s.CreateOffHoursSchedule(ctx, newSchedule("one", "env", "dev"), 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("same name = %v, want ErrConflict", err)
	}
	other, err := s.CreateOffHoursSchedule(ctx, newSchedule("three", "env", "dev"), 0)
	if err != nil {
		t.Fatal(err)
	}
	clash := other
	clash.TagValue = "staging"
	if _, err := s.UpdateOffHoursSchedule(ctx, clash, 0); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("update onto an existing selector = %v, want ErrConflict", err)
	}
}

func TestOffHoursMatches_ExactTagOnly(t *testing.T) {
	s, ctx := storetest.Open(t), context.Background()
	want := seedTaggedService(t, s, "match", "ACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "longer-value", "ACTIVE", `[{"Key":"env","Value":"staging-old"}]`, "", "")
	seedTaggedService(t, s, "other-key-case", "ACTIVE", `[{"Key":"Env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "inactive", "INACTIVE", `[{"Key":"env","Value":"staging"}]`, "", "")
	seedTaggedService(t, s, "untagged", "ACTIVE", `[]`, "", "")

	got, err := s.OffHoursMatches(ctx, "env", "staging")
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

	got, err := s.OffHoursMatches(ctx, "env", "staging")
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

	got, err := s.OffHoursTagValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[[2]string]int{}
	for _, v := range got {
		counts[[2]string{v.Key, v.Value}] = v.Services
	}
	want := map[[2]string]int{{"env", "staging"}: 2, {"env", "dev"}: 1, {"team", "x"}: 1}
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
	if err == nil {
		t.Fatal("second lock succeeded while the first transaction was open")
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
