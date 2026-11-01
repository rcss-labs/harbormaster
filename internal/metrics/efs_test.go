package metrics

import "testing"

func TestEFSStorageQueries_IDsAndDimensions(t *testing.T) {
	qs := EFSStorageQueries([]string{"fs-02e7d1c9", "fs-0a6b33f0"})
	if len(qs) != 2 {
		t.Fatalf("len = %d", len(qs))
	}
	for i, q := range qs {
		if !queryIDPattern.MatchString(q.ID) {
			t.Errorf("id %q invalid", q.ID)
		}
		if q.Namespace != "AWS/EFS" || q.MetricName != "StorageBytes" || q.Stat != "Average" || q.Period != 86400 {
			t.Errorf("query = %+v", q)
		}
		dims := map[string]string{}
		for _, d := range q.Dimensions {
			dims[d.Name] = d.Value
		}
		if dims["StorageClass"] != "Total" || dims["FileSystemId"] != []string{"fs-02e7d1c9", "fs-0a6b33f0"}[i] {
			t.Errorf("dims = %v", dims)
		}
	}
	if qs[0].ID != "sz_0" || qs[1].ID != "sz_1" {
		t.Errorf("ids = %s,%s", qs[0].ID, qs[1].ID)
	}
}

func TestEFSQueries_OnePeriodPerBuilder(t *testing.T) {
	ids := []string{"fs-a", "fs-b", "fs-c"}
	for name, qs := range map[string][]Query{"size": EFSStorageQueries(ids), "io": EFSThroughputQueries(ids), "cc": EFSConnectionQueries(ids)} {
		for _, q := range qs {
			if q.Period != qs[0].Period || !queryIDPattern.MatchString(q.ID) {
				t.Errorf("%s: mixed period or bad id: %+v", name, q)
			}
		}
	}
	if q := EFSThroughputQueries(ids)[1]; q.ID != "io_1" || q.MetricName != "MeteredIOBytes" || q.Stat != "Sum" || q.Period != 1200 {
		t.Errorf("io = %+v", q)
	}
	if q := EFSConnectionQueries(ids)[2]; q.ID != "cc_2" || q.MetricName != "ClientConnections" || q.Stat != "Sum" || q.Period != 60 {
		t.Errorf("cc = %+v", q)
	}
}

func TestBytesPerPeriodToMiBps(t *testing.T) {
	if got := BytesPerPeriodToMiBps(1258291200, 1200); got != 1.0 {
		t.Fatalf("got %v, want 1.0", got)
	}
}
