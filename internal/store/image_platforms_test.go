package store_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/store/storetest"
)

// addService stores an ACTIVE service whose task definition has the given compatibilities, platform and images.
func addService(t *testing.T, s *store.Store, cluster, name, status string, compat []string, arch, osFamily string, images ...string) {
	t.Helper()
	key := serviceKey(cluster, name)
	arn := "arn:aws:ecs:eu-west-1:" + key.AccountID + ":task-definition/" + name + ":1"
	var containers []map[string]any
	for i, image := range images {
		containers = append(containers, map[string]any{"Name": name + string(rune('a'+i)), "Image": image})
	}
	raw, _ := json.Marshal(map[string]any{
		"RuntimePlatform":      map[string]string{"CpuArchitecture": arch, "OperatingSystemFamily": osFamily},
		"ContainerDefinitions": containers,
	})
	ctx := context.Background()
	if err := s.UpsertTaskDefinition(ctx, model.TaskDefinitionRef{
		AccountID: key.AccountID, Region: key.Region, ARN: arn, Family: name, Revision: 1,
		CPU: "256", Memory: "512", RequiresCompatibilities: compat, Raw: raw,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServices(ctx, []model.Service{{Key: key, Status: status, Desired: 1, TaskDefinitionARN: arn, PolledAt: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
}

func refImages(t *testing.T, s *store.Store) []string {
	t.Helper()
	rows, err := s.Pool.Query(context.Background(), `select image from image_refs order by image`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var image string
		if err := rows.Scan(&image); err != nil {
			t.Fatal(err)
		}
		out = append(out, image)
	}
	return out
}

func TestTrackImages_OnlyX86FargateLinuxServices(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	fargate := []string{"FARGATE"}
	addService(t, s, "c", "tracked", "ACTIVE", fargate, "X86_64", "LINUX", "nginx:1", "123.dkr.ecr.eu-west-1.amazonaws.com/side:2")
	addService(t, s, "c", "default-arch", "ACTIVE", fargate, "", "", "nginx:1") // no platform set means x86 Linux, and shares nginx:1
	addService(t, s, "c", "ec2", "ACTIVE", []string{"EC2"}, "X86_64", "LINUX", "ec2-image:1")
	addService(t, s, "c", "both", "ACTIVE", []string{"EC2", "FARGATE"}, "X86_64", "LINUX", "both-image:1")
	addService(t, s, "c", "windows", "ACTIVE", fargate, "X86_64", "WINDOWS_SERVER_2019_CORE", "win-image:1")
	addService(t, s, "c", "arm", "ACTIVE", fargate, "ARM64", "LINUX", "arm-image:1")
	addService(t, s, "c", "gone", "INACTIVE", fargate, "X86_64", "LINUX", "gone-image:1")

	added, err := s.TrackX86FargateImages(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"123.dkr.ecr.eu-west-1.amazonaws.com/side:2", "both-image:1", "nginx:1"}
	if got := refImages(t, s); !reflect.DeepEqual(got, want) || added != 3 {
		t.Fatalf("tracked %v (added %d), want %v: EC2-only, Windows, ARM64 and inactive services contribute nothing and a shared image is one row, and a definition that lists FARGATE next to EC2 counts as Fargate like the Cost advisors say", got, added, want)
	}
	var status string
	var due time.Time
	if err := s.Pool.QueryRow(ctx, `select status, next_check_at from image_refs where image = 'nginx:1'`).Scan(&status, &due); err != nil || status != "pending" {
		t.Fatalf("new ref = %q, %v; want pending", status, err)
	}
	again, err := s.TrackX86FargateImages(ctx, time.Now())
	if err != nil || again != 0 {
		t.Fatalf("second call added %d, %v; want none (only last_seen_at moves)", again, err)
	}
}

func TestDueImageRefs_OrderAndLimit(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for image, due := range map[string]time.Duration{"b:1": -time.Hour, "a:1": -2 * time.Hour, "c:1": time.Hour, "d:1": -time.Hour} {
		if _, err := s.Pool.Exec(ctx, `insert into image_refs (image, registry, repository, status, next_check_at, last_seen_at) values ($1, '', '', 'pending', $2, $3)`,
			image, now.Add(due), now); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DueImageRefs(ctx, now, 2)
	if err != nil || len(got) != 2 || got[0].Image != "a:1" || got[1].Image != "b:1" {
		t.Fatalf("due = %+v, %v; want a:1 then b:1 (oldest due first, ties by name, limited, not-yet-due left out)", got, err)
	}
}

func TestSaveImageRefResult_KeepsDigestAndSchedulesNext(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := s.Pool.Exec(ctx, `insert into image_refs (image, registry, repository, status, next_check_at, last_seen_at) values ('x:1', '', '', 'pending', $1, $1)`, now); err != nil {
		t.Fatal(err)
	}
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := s.SaveImageRefResult(ctx, store.ImageRef{Image: "x:1", Registry: "reg", Repository: "repo", Digest: digest, Status: "ok", CheckedAt: &now}, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A later failure carries no digest: the last known one stays, and so does what was checked.
	if err := s.SaveImageRefResult(ctx, store.ImageRef{Image: "x:1", Registry: "reg", Repository: "repo", Status: "error", Detail: "123456789012"}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var gotDigest, status, detail string
	var next time.Time
	if err := s.Pool.QueryRow(ctx, `select coalesce(digest, ''), status, coalesce(detail, ''), next_check_at from image_refs where image = 'x:1'`).Scan(&gotDigest, &status, &detail, &next); err != nil {
		t.Fatal(err)
	}
	if gotDigest != digest || status != "error" || detail != "123456789012" || !next.Equal(now.Add(time.Hour)) {
		t.Fatalf("ref = %q %q %q next %v; want the digest kept, the new status and detail, next check in an hour", gotDigest, status, detail, next)
	}
	if err := s.SaveImageRefResult(ctx, store.ImageRef{Image: "x:1", Status: "bogus"}, now); err == nil {
		t.Fatal("an unknown status was stored")
	}
}

func TestImagePlatformsFor_JoinsManifestByDigest(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, image := range []string{"known:1", "unknown:1", "unresolved:1"} {
		if _, err := s.Pool.Exec(ctx, `insert into image_refs (image, registry, repository, status, next_check_at, last_seen_at) values ($1, 'reg', $1, 'pending', $2, $2)`, image, now); err != nil {
			t.Fatal(err)
		}
	}
	for image, d := range map[string]string{"known:1": digest, "unknown:1": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"} {
		if _, err := s.Pool.Exec(ctx, `update image_refs set digest = $2, status = 'ok' where image = $1`, image, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SaveImageManifest(ctx, "reg", "known:1", digest, "application/vnd.oci.image.index.v1+json", true, []string{"linux/amd64", "linux/arm64"}, now); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.HasImageManifest(ctx, "reg", "known:1", digest); err != nil || !ok {
		t.Fatalf("HasImageManifest = %v, %v", ok, err)
	}
	if ok, _ := s.HasImageManifest(ctx, "reg", "known:1", "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); ok {
		t.Fatal("HasImageManifest true for a digest never saved")
	}

	got, err := s.ImagePlatformsFor(ctx, []string{"known:1", "unknown:1", "unresolved:1", "never-tracked:1"})
	if err != nil {
		t.Fatal(err)
	}
	known := got["known:1"]
	sort.Strings(known.Platforms)
	if !known.Known || !known.IsIndex || !reflect.DeepEqual(known.Platforms, []string{"linux/amd64", "linux/arm64"}) || known.Status != "ok" {
		t.Fatalf("known = %+v", known)
	}
	if u := got["unknown:1"]; u.Known || u.Digest == "" {
		t.Fatalf("unknown = %+v, want a digest without a manifest row to be not Known", u)
	}
	if u := got["unresolved:1"]; u.Known || u.Status != "pending" {
		t.Fatalf("unresolved = %+v", u)
	}
	if _, ok := got["never-tracked:1"]; ok {
		t.Fatal("an image that is not tracked was returned")
	}
}

func TestPruneImagePlatforms_DropsUnseenRefsAndOrphanManifests(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, fresh := now.Add(-10*24*time.Hour), now
	digestOld := "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	digestNew := "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	for image, seen := range map[string]time.Time{"old:1": old, "new:1": fresh} {
		d := digestOld
		if image == "new:1" {
			d = digestNew
		}
		if _, err := s.Pool.Exec(ctx, `insert into image_refs (image, registry, repository, digest, status, next_check_at, last_seen_at) values ($1, 'reg', $1, $2, 'ok', $3, $3)`, image, d, seen); err != nil {
			t.Fatal(err)
		}
		if err := s.SaveImageManifest(ctx, "reg", image, d, "m", false, nil, seen); err != nil {
			t.Fatal(err)
		}
	}
	// A manifest no ref points to, whatever its age, is an orphan too.
	if err := s.SaveImageManifest(ctx, "reg", "orphan:1", "sha256:1111111111111111111111111111111111111111111111111111111111111111", "m", false, nil, fresh); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.PruneImagePlatforms(ctx, now.Add(-7*24*time.Hour))
	if err != nil || deleted != 3 {
		t.Fatalf("deleted %d, %v; want the stale ref and two orphan manifests", deleted, err)
	}
	if got := refImages(t, s); !reflect.DeepEqual(got, []string{"new:1"}) {
		t.Fatalf("refs left = %v, want new:1", got)
	}
	var manifests int
	if err := s.Pool.QueryRow(ctx, `select count(*) from image_manifests`).Scan(&manifests); err != nil || manifests != 1 {
		t.Fatalf("manifests = %d, %v; want only the one a ref points to", manifests, err)
	}
}

func TestRunRetention_PrunesImagePlatformsAfterAWeek(t *testing.T) {
	s := storetest.Open(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := s.Pool.Exec(ctx, `insert into image_refs (image, registry, repository, status, next_check_at, last_seen_at) values ('stale:1', '', '', 'pending', $1, $1), ('live:1', '', '', 'pending', $2, $2)`,
		now.Add(-8*24*time.Hour), now.Add(-6*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	result, err := s.RunRetention(ctx, now, store.Retention{})
	if err != nil {
		t.Fatal(err)
	}
	if result["image_platforms"] != 1 {
		t.Fatalf("retention result = %v, want one image_platforms row deleted", result)
	}
	if got := refImages(t, s); !reflect.DeepEqual(got, []string{"live:1"}) {
		t.Fatalf("refs left = %v", got)
	}
}
