package snapcache

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func inflate(t *testing.T, body []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestModels_CarryNoTagOrPermissionFields(t *testing.T) {
	f := newEstateFixture(t)
	ctx := context.Background()
	// Tag everything, so the rows the loaders read from SQL carry real tag ids and the tags have names to look for.
	for i, k := range f.clusters {
		if err := f.st.SetClusterTag(ctx, k, f.tagIDs[1+i%3]); err != nil {
			t.Fatal(err)
		}
	}
	own := f.tagIDs[2]
	if err := f.st.SetServiceTag(ctx, f.services[0], &own); err != nil {
		t.Fatal(err)
	}
	cluster, err := LoadCluster(ctx, f.st, f.clusters[0])
	if err != nil {
		t.Fatal(err)
	}
	service, err := LoadService(ctx, f.st, f.services[0])
	if err != nil {
		t.Fatal(err)
	}
	slice, err := LoadSlice(ctx, f.st, targetOf(f.slices()[0]))
	if err != nil {
		t.Fatal(err)
	}
	// Rows that still carry tag data (as a filtered model does) must not serialise it either.
	filled := ClusterModel{Cluster: store.ClusterRow{TagID: 4242, TagSet: true},
		Services: []store.ServiceRow{{EffectiveTagID: 4242, TagInherited: true}},
		Tasks:    []store.TaskRow{{EffectiveTagID: 4242}}}

	forbidden := []string{"zz-prod-7f3a", "zz-staging-7f3a", "zz-data-7f3a", "4242", "TagID", "TagSet", "TagInherited", "EffectiveTagID", "Perms", "UserID", "Username", "Admin"}
	for name, m := range map[string]any{"cluster": cluster, "service": service, "slice": slice, "filled": filled} {
		body, err := Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		text := inflate(t, body)
		for _, bad := range forbidden {
			if strings.Contains(text, bad) {
				t.Errorf("the %s model contains %q", name, bad)
			}
		}
	}
	if cluster.Cluster.TagID != 0 || cluster.Cluster.TagSet || cluster.Services[0].EffectiveTagID != 0 || service.Service.EffectiveTagID != 0 || slice.Tasks[0].EffectiveTagID != 0 {
		t.Error("a loader returned a row with a tag filled in")
	}
}

func TestModels_LeaveOutRawDocumentsNoPageRenders(t *testing.T) {
	f := newEstateFixture(t)
	ctx := context.Background()
	cluster, _ := LoadCluster(ctx, f.st, f.clusters[0])
	service, _ := LoadService(ctx, f.st, f.services[0])
	if cluster.Cluster.Raw != nil || cluster.Services[0].Raw != nil || cluster.Tasks[0].Raw != nil {
		t.Error("the cluster model carries a raw document")
	}
	if service.Tasks[0].Raw != nil {
		t.Error("the service model carries a task's raw document")
	}
	if len(service.Service.Raw) == 0 {
		t.Error("the service model dropped the raw document the page parses")
	}
	if len(service.Tasks[0].Containers) == 0 {
		t.Error("the service model dropped the containers the page reads for the exec agent")
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	f := newEstateFixture(t)
	m, err := LoadCluster(context.Background(), f.st, f.clusters[0])
	if err != nil {
		t.Fatal(err)
	}
	body, err := Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Fatalf("body does not start with the gzip magic: % x", body[:min(len(body), 4)])
	}
	var back ClusterModel
	if err := Decode(body, &back); err != nil {
		t.Fatal(err)
	}
	if jsonOf(t, back) != jsonOf(t, m) {
		t.Fatalf("round trip changed the model\n got: %.300s\nwant: %.300s", jsonOf(t, back), jsonOf(t, m))
	}
	if len(back.Tasks) == 0 || len(back.Services) == 0 {
		t.Fatal("the fixture cluster has no rows: the round trip proves nothing")
	}
}

func TestCodec_RejectsGarbage(t *testing.T) {
	good, err := Encode(ClusterModel{Services: []store.ServiceRow{{Service: model.Service{Key: model.ServiceKey{Service: "x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	notGzip := []byte("plain text, not a model")
	badJSON, _ := Encode("just a string")
	for name, body := range map[string][]byte{
		"empty":      nil,
		"not gzip":   notGzip,
		"truncated":  good[:len(good)/2],
		"wrong type": badJSON,
		"unknown field": func() []byte {
			b, _ := Encode(map[string]any{"Surprise": 1})
			return b
		}(),
	} {
		var m ClusterModel
		if err := Decode(body, &m); err == nil {
			t.Errorf("%s: Decode accepted it", name)
		}
	}
}

var _ = authz.TagID(0)
