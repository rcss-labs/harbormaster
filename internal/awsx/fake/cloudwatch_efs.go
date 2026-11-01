package fake

import (
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

const efsSizeWindow = 30 * 24 * time.Hour

// efsPoint builds the value function for an AWS/EFS metric. Caller holds Mu.
//   - StorageBytes rises linearly from 78 % to 100 % of the seeded size over
//     30 days with +-1 % noise; the latest point is exactly the seeded size.
//   - MeteredIOBytes averages 25 MiB/s (size > 10 GiB) or 4 MiB/s, +-50 %.
//   - ClientConnections is the number of running tasks mounting the file system.
func (c *cloudWatchClient) efsPoint(metric string, dims map[string]string, period time.Duration) func(time.Time, float64) float64 {
	e := c.f.Estate
	fsID := dims["FileSystemId"]
	var size float64
	found := false
	for _, fs := range e.Network.FileSystems {
		if fs.AccountID == c.target.AccountID && fs.Region == c.target.Region && aws.ToString(fs.FileSystem.FileSystemId) == fsID {
			size, found = float64(fs.FileSystem.SizeInBytes.Value), true
		}
	}
	if !found {
		return nil
	}
	now := e.now()
	switch metric {
	case "StorageBytes":
		return func(ts time.Time, u float64) float64 {
			age := now.Sub(ts)
			if age <= period {
				return size
			}
			progress := 1 - min(1, float64(age)/float64(efsSizeWindow))
			return size * (0.78 + 0.22*progress) * (1 + (u-0.5)*0.02)
		}
	case "MeteredIOBytes":
		mibPerSecond := 4.0
		if size > 10*(1<<30) {
			mibPerSecond = 25
		}
		perPeriod := mibPerSecond * (1 << 20) * period.Seconds()
		return func(_ time.Time, u float64) float64 { return noisy(perPeriod, perPeriod*0.5, u) }
	case "ClientConnections":
		n := float64(c.efsMountingRunningTasks(fsID))
		return func(time.Time, float64) float64 { return n }
	}
	return nil
}

// efsMountingRunningTasks counts running tasks of this target whose task
// definition mounts the file system. Caller holds Mu.
func (c *cloudWatchClient) efsMountingRunningTasks(fsID string) int {
	e := c.f.Estate
	mounting := map[string]bool{}
	for _, td := range e.TaskDefinitions {
		if td.AccountID != c.target.AccountID || td.Region != c.target.Region {
			continue
		}
		for _, v := range td.Definition.Volumes {
			if v.EfsVolumeConfiguration != nil && aws.ToString(v.EfsVolumeConfiguration.FileSystemId) == fsID {
				mounting[aws.ToString(td.Definition.TaskDefinitionArn)] = true
			}
		}
	}
	n := 0
	for _, t := range e.Tasks {
		if t.AccountID == c.target.AccountID && t.Region == c.target.Region &&
			strings.EqualFold(aws.ToString(t.Task.LastStatus), string(ecstypes.DesiredStatusRunning)) &&
			mounting[aws.ToString(t.Task.TaskDefinitionArn)] {
			n++
		}
	}
	return n
}
