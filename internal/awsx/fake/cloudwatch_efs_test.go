package fake

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

func efsQuery(id, metric, stat, fsID string, period int32) cwtypes.MetricDataQuery {
	return cwtypes.MetricDataQuery{Id: aws.String(id), MetricStat: &cwtypes.MetricStat{
		Metric: &cwtypes.Metric{Namespace: aws.String("AWS/EFS"), MetricName: aws.String(metric), Dimensions: []cwtypes.Dimension{
			{Name: aws.String("FileSystemId"), Value: aws.String(fsID)}, {Name: aws.String("StorageClass"), Value: aws.String("Total")}}},
		Period: aws.Int32(period), Stat: aws.String(stat)}}
}

func efsFetch(t *testing.T, q cwtypes.MetricDataQuery, window time.Duration) cwtypes.MetricDataResult {
	t.Helper()
	// shared-certs (fs-0a6b33f0) lives in eu-central-1, every other fs in us-east-1 or eu-west-1.
	target := homelabUSEast1
	if q.MetricStat.Metric.Dimensions[0].Value != nil && *q.MetricStat.Metric.Dimensions[0].Value == "fs-0a6b33f0" {
		target.Region = "eu-central-1"
	}
	api := cloudWatchFor(t, newFake(), target)
	out, err := api.GetMetricData(context.Background(), &cloudwatch.GetMetricDataInput{
		MetricDataQueries: []cwtypes.MetricDataQuery{q}, StartTime: aws.Time(fixedNow.Add(-window)), EndTime: aws.Time(fixedNow),
		ScanBy: cwtypes.ScanByTimestampAscending})
	if err != nil {
		t.Fatal(err)
	}
	return out.MetricDataResults[0]
}

func seededSize(t *testing.T, fsID string) float64 {
	t.Helper()
	f := newFake()
	for _, fs := range f.Estate.Network.FileSystems {
		if aws.ToString(fs.FileSystem.FileSystemId) == fsID {
			return float64(fs.FileSystem.SizeInBytes.Value)
		}
	}
	t.Fatalf("no seeded fs %s", fsID)
	return 0
}

func TestFakeCloudWatch_EFSStorageBytesEndsAtSeededSize(t *testing.T) {
	const fsID = "fs-02e7d1c9"
	r := efsFetch(t, efsQuery("sz_0", "StorageBytes", "Average", fsID, 86400), 30*24*time.Hour)
	size := seededSize(t, fsID)
	n := len(r.Values)
	if n != 30 {
		t.Fatalf("points = %d, want 30", n)
	}
	if r.Values[n-1] != size {
		t.Fatalf("last = %v, want seeded %v", r.Values[n-1], size)
	}
	if first := r.Values[0]; first < 0.76*size || first > 0.80*size {
		t.Fatalf("first = %v, want ~78%% of %v", first, size)
	}
}

func TestFakeCloudWatch_EFSMeteredIOBytesScalesWithSize(t *testing.T) {
	big := efsFetch(t, efsQuery("io_0", "MeteredIOBytes", "Sum", "fs-02e7d1c9", 1200), 24*time.Hour)
	small := efsFetch(t, efsQuery("io_0", "MeteredIOBytes", "Sum", "fs-0a6b33f0", 1200), 24*time.Hour)
	mibps := func(v float64) float64 { return v / 1200 / (1 << 20) }
	for _, v := range big.Values {
		if v <= 0 {
			t.Fatalf("non-positive io %v", v)
		}
	}
	// 12.1 GiB > 10 GiB -> ~25 MiB/s +-50 %; 0.02 GiB -> ~4 MiB/s +-50 %.
	if m := mibps(big.Values[5]); m < 12 || m > 38 {
		t.Errorf("big MiB/s = %v", m)
	}
	if m := mibps(small.Values[5]); m < 2 || m > 6 {
		t.Errorf("small MiB/s = %v", m)
	}
}

func TestFakeCloudWatch_EFSClientConnections(t *testing.T) {
	cases := map[string]float64{"fs-0a6b33f0": 3, "fs-02e7d1c9": 1}
	for fsID, want := range cases {
		r := efsFetch(t, efsQuery("cc_0", "ClientConnections", "Sum", fsID, 60), 10*time.Minute)
		if len(r.Values) == 0 || r.Values[len(r.Values)-1] != want {
			t.Errorf("%s: values = %v, want %v", fsID, r.Values, want)
		}
	}
}
