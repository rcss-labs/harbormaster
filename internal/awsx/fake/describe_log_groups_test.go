package fake

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

func TestFakeDescribeLogGroups_ReportsStoredBytes(t *testing.T) {
	f := newFake()
	api := logsFor(t, f, homelabUSEast1)
	f.Estate.SetLogGroupStoredBytes("/ecs/test/big", 3<<30)
	f.Estate.SetLogLines("/ecs/test/small", []LogLine{{Stream: "a/b/c", Timestamp: fixedNow.Add(-time.Minute), Message: "hello"}})

	out, err := api.DescribeLogGroups(context.Background(), &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String("/ecs/test/")})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, g := range out.LogGroups {
		got[aws.ToString(g.LogGroupName)] = aws.ToInt64(g.StoredBytes)
	}
	if got["/ecs/test/big"] != 3<<30 {
		t.Errorf("big stored bytes = %d, want %d", got["/ecs/test/big"], int64(3<<30))
	}
	if got["/ecs/test/small"] != int64(len("hello")+insightsLineOverhead) {
		t.Errorf("small stored bytes = %d, want %d", got["/ecs/test/small"], len("hello")+insightsLineOverhead)
	}
}
