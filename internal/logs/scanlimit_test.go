package logs

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	logstypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
)

func scriptedResults(status logstypes.QueryStatus, bytesScanned float64) *cloudwatchlogs.GetQueryResultsOutput {
	return &cloudwatchlogs.GetQueryResultsOutput{
		Status:     status,
		Results:    [][]logstypes.ResultField{resultRow("2026-10-31 11:59:00.000", "partial", homelabAccount+":/g/a", "api/web/t1")},
		Statistics: &logstypes.QueryStatistics{RecordsMatched: 7, BytesScanned: bytesScanned},
	}
}

func TestSearcher_StopsQueryWhenBytesScannedPassTheCap(t *testing.T) {
	s, spy := newSearcher(newSearchFake())
	s.MaxBytes = 1000
	spy.scripted = scriptedResults(logstypes.QueryStatusRunning, 2000)

	res, err := s.Search(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "api/web/")))
	if err != nil {
		t.Fatal(err)
	}
	if len(spy.stopCtxErrs) != 1 {
		t.Errorf("StopQuery calls = %d, want 1", len(spy.stopCtxErrs))
	}
	if !res.ScanLimitReached {
		t.Error("ScanLimitReached = false, want true")
	}
	tr := res.Targets[0]
	if tr.State != StateScanLimit || tr.BytesScanned != 2000 {
		t.Errorf("target state=%s bytes=%d, want %s and the bytes seen (2000)", tr.State, tr.BytesScanned, StateScanLimit)
	}
	if len(res.Rows) != 1 || res.Rows[0].Message != "partial" || res.BytesScanned != 2000 {
		t.Errorf("rows=%+v bytes=%d, want the partial row and 2000 bytes", res.Rows, res.BytesScanned)
	}
}

func TestSearcher_CapCountsBytesAcrossBatches(t *testing.T) {
	// Two targets, each under the cap alone (600 < 1000) but 1200 together.
	s, spy := newSearcher(newSearchFake())
	s.MaxBytes = 1000
	spy.scripted = scriptedResults(logstypes.QueryStatusRunning, 600)

	res, err := s.Search(context.Background(), searchReq(
		svc("A", homelabAccount, "us-east-1", "/g/a", "api/web/"),
		svc("B", stagingAccount, "us-east-1", "/g/b", "api/web/")))
	if err != nil {
		t.Fatal(err)
	}
	if !res.ScanLimitReached {
		t.Error("ScanLimitReached = false, want true once the batches together pass the cap")
	}
}

func TestSearcher_DoesNotStopUnderTheCapOrWithoutOne(t *testing.T) {
	for name, maxBytes := range map[string]int64{"under the cap": 1 << 20, "no cap": 0} {
		t.Run(name, func(t *testing.T) {
			s, spy := newSearcher(newSearchFake())
			s.MaxBytes = maxBytes
			bytesScanned := 500.0
			if maxBytes == 0 {
				bytesScanned = 1 << 40
			}
			spy.scripted = scriptedResults(logstypes.QueryStatusComplete, bytesScanned)

			res, err := s.Search(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "api/web/")))
			if err != nil {
				t.Fatal(err)
			}
			if len(spy.stopCtxErrs) != 0 || res.ScanLimitReached || res.Targets[0].State != StateOK {
				t.Errorf("stops=%d limit=%v state=%s, want none, false, ok", len(spy.stopCtxErrs), res.ScanLimitReached, res.Targets[0].State)
			}
		})
	}
}

func TestSearcher_EstimateSumsStoredBytesOncePerGroup(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogGroupStoredBytes("/g/a", 1000)
	f.Estate.SetLogGroupStoredBytes("/g/shared", 400)
	f.Estate.SetLogGroupStoredBytes("/g/stag", 50)
	s, _ := newSearcher(f)

	est, err := s.Estimate(context.Background(), searchReq(
		svc("A", homelabAccount, "us-east-1", "/g/a", "a/"),
		svc("S1", homelabAccount, "us-east-1", "/g/shared", "s1/"),
		svc("S2", homelabAccount, "us-east-1", "/g/shared", "s2/"),
		svc("T", stagingAccount, "us-east-1", "/g/stag", "t/"),
		svc("Gone", stagingAccount, "us-east-1", "/g/missing", "x/")))
	if err != nil {
		t.Fatal(err)
	}
	if est.StoredBytes != 1450 {
		t.Errorf("StoredBytes = %d, want 1450 (shared group counted once)", est.StoredBytes)
	}
	if est.Groups != 4 || est.UnknownGroups != 1 {
		t.Errorf("groups=%d unknown=%d, want 4 groups of which 1 unknown", est.Groups, est.UnknownGroups)
	}
}

func TestSearcher_EstimateOnlyCoversGivenServices(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogGroupStoredBytes("/g/a", 1000)
	f.Estate.SetLogGroupStoredBytes("/g/other", 999999)
	s, spy := newSearcher(f)
	est, err := s.Estimate(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "a/")))
	if err != nil {
		t.Fatal(err)
	}
	if est.StoredBytes != 1000 {
		t.Errorf("StoredBytes = %d, want 1000", est.StoredBytes)
	}
	if len(spy.starts) != 0 {
		t.Error("an estimate must not start a Logs Insights query")
	}
}

func TestSearcher_EstimateFailedTargetIsUnknownNotAnError(t *testing.T) {
	f := newSearchFake()
	f.Estate.SetLogGroupStoredBytes("/g/a", 1000)
	s, spy := newSearcher(f)
	spy.failLogs = errAssume{}
	est, err := s.Estimate(context.Background(), searchReq(svc("A", homelabAccount, "us-east-1", "/g/a", "a/")))
	if err != nil {
		t.Fatal(err)
	}
	if est.StoredBytes != 0 || est.UnknownGroups != 1 {
		t.Errorf("bytes=%d unknown=%d, want 0 and 1", est.StoredBytes, est.UnknownGroups)
	}
}

type errAssume struct{}

func (errAssume) Error() string { return "assume role failed" }
