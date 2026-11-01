package fake

import (
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

// plan 06: estates at target scale for the soak test.

// Scale sizes a generated estate. Every account gets RegionsPerAccount regions
// (at most len(generatedRegions)), every region ClustersPerRegion clusters,
// and so on down to running tasks.
type Scale struct{ Accounts, RegionsPerAccount, ClustersPerRegion, ServicesPerCluster, TasksPerService int }

// TargetScale is the design target (§5.3): 20 account x region loops, 400
// clusters, 6000 services, 18000 running tasks.
var TargetScale = Scale{Accounts: 5, RegionsPerAccount: 4, ClustersPerRegion: 20, ServicesPerCluster: 15, TasksPerService: 3}

var generatedRegions = []string{"us-east-1", "eu-west-1", "eu-central-1", "us-west-2", "ap-southeast-2", "eu-west-2"}

// Generate builds a deterministic estate: accounts "1000000000NN", clusters
// "cluster-NN" per region, services "svc-NNN" (unique within an account and
// region, numbered across its clusters) each with one task definition revision
// and TasksPerService RUNNING tasks. Same seed and scale give the same ARNs.
// It sets only ECS state; the observe, network and storage state stays empty,
// which those poller tiers treat as "nothing found".
func Generate(seed int64, s Scale) *Estate {
	now := time.Now()
	rng := rand.New(rand.NewPCG(uint64(seed), 0x9e3779b97f4a7c15))
	hex := func(n int) string {
		const digits = "0123456789abcdef"
		b := make([]byte, n)
		for i := range b {
			b[i] = digits[rng.IntN(16)]
		}
		return string(b)
	}
	sizes := [][2]string{{"256", "512"}, {"512", "1024"}, {"1024", "2048"}}

	e := &Estate{Events: map[string][]ecstypes.ServiceEvent{}}
	regionsPerAccount := min(s.RegionsPerAccount, len(generatedRegions))
	e.Clusters = make([]Cluster, 0, s.Accounts*regionsPerAccount*s.ClustersPerRegion)
	e.Services = make([]Service, 0, cap(e.Clusters)*s.ServicesPerCluster)
	e.TaskDefinitions = make([]TaskDefinition, 0, cap(e.Services))
	e.Tasks = make([]Task, 0, cap(e.Services)*s.TasksPerService)

	for a := 1; a <= s.Accounts; a++ {
		account := fmt.Sprintf("1000000000%02d", a)
		acct := Account{ID: account, Name: fmt.Sprintf("generated-%02d", a), Regions: append([]string(nil), generatedRegions[:regionsPerAccount]...)}
		e.Accounts = append(e.Accounts, acct)
		for _, region := range acct.Regions {
			for c := 0; c < s.ClustersPerRegion; c++ {
				cluster := fmt.Sprintf("cluster-%02d", c)
				e.Clusters = append(e.Clusters, Cluster{AccountID: account, Region: region, Cluster: ecstypes.Cluster{
					ClusterArn:  aws.String(clusterARN(region, account, cluster)),
					ClusterName: aws.String(cluster),
					Status:      aws.String("ACTIVE"),
				}})
				for i := 0; i < s.ServicesPerCluster; i++ {
					name := fmt.Sprintf("svc-%03d", c*s.ServicesPerCluster+i)
					size := sizes[rng.IntN(len(sizes))]
					tdARN := taskDefinitionARN(region, account, name, 1)
					e.TaskDefinitions = append(e.TaskDefinitions, TaskDefinition{AccountID: account, Region: region, Definition: ecstypes.TaskDefinition{
						Family: aws.String(name), Revision: 1, TaskDefinitionArn: aws.String(tdARN),
						NetworkMode:             ecstypes.NetworkModeAwsvpc,
						RequiresCompatibilities: []ecstypes.Compatibility{ecstypes.CompatibilityFargate},
						Cpu:                     aws.String(size[0]), Memory: aws.String(size[1]),
						Status:       ecstypes.TaskDefinitionStatusActive,
						RegisteredAt: aws.Time(now.Add(-48 * time.Hour)),
						ContainerDefinitions: []ecstypes.ContainerDefinition{{
							Name: aws.String(name), Image: aws.String(account + ".dkr.ecr." + region + ".amazonaws.com/" + name + ":1.0.0"),
							Essential: aws.Bool(true),
						}},
					}})
					deploymentID := "ecs-svc/" + fmt.Sprintf("%019d", rng.Uint64()%10_000_000_000_000_000_000)
					e.Services = append(e.Services, Service{AccountID: account, Region: region, ClusterName: cluster, Service: ecstypes.Service{
						ServiceArn:   aws.String(serviceARN(region, account, cluster, name)),
						ServiceName:  aws.String(name),
						ClusterArn:   aws.String(clusterARN(region, account, cluster)),
						Status:       aws.String("ACTIVE"),
						DesiredCount: int32(s.TasksPerService), RunningCount: int32(s.TasksPerService),
						LaunchType:         ecstypes.LaunchTypeFargate,
						SchedulingStrategy: ecstypes.SchedulingStrategyReplica,
						TaskDefinition:     aws.String(tdARN),
						CreatedAt:          aws.Time(now.Add(-72 * time.Hour)),
						Deployments: []ecstypes.Deployment{{
							Id: aws.String(deploymentID), Status: aws.String("PRIMARY"), TaskDefinition: aws.String(tdARN),
							DesiredCount: int32(s.TasksPerService), RunningCount: int32(s.TasksPerService),
							RolloutState: ecstypes.DeploymentRolloutStateCompleted,
							CreatedAt:    aws.Time(now.Add(-48 * time.Hour)), UpdatedAt: aws.Time(now.Add(-48 * time.Hour)),
						}},
					}})
					for k := 0; k < s.TasksPerService; k++ {
						id := hex(32)
						arn := taskARN(region, account, cluster, id)
						started := now.Add(-time.Duration(1+rng.IntN(48)) * time.Hour)
						e.Tasks = append(e.Tasks, Task{AccountID: account, Region: region, ClusterName: cluster, Task: ecstypes.Task{
							TaskArn: aws.String(arn), ClusterArn: aws.String(clusterARN(region, account, cluster)),
							TaskDefinitionArn: aws.String(tdARN),
							LastStatus:        aws.String("RUNNING"), DesiredStatus: aws.String("RUNNING"),
							HealthStatus: ecstypes.HealthStatusHealthy, LaunchType: ecstypes.LaunchTypeFargate,
							Cpu: aws.String(size[0]), Memory: aws.String(size[1]),
							Group: aws.String("service:" + name), StartedBy: aws.String(deploymentID),
							Connectivity: ecstypes.ConnectivityConnected, Version: 1,
							CreatedAt: aws.Time(started.Add(-30 * time.Second)), StartedAt: aws.Time(started),
							Containers: []ecstypes.Container{{
								ContainerArn: aws.String(arn + "/c0"), TaskArn: aws.String(arn), Name: aws.String(name),
								LastStatus: aws.String("RUNNING"), HealthStatus: ecstypes.HealthStatusHealthy,
							}},
						}})
					}
				}
			}
		}
	}
	return e
}

// MutateTasks changes n random RUNNING tasks of the target (all of them if it
// has fewer) so their normalized form, and so their stored hash, changes:
// health status flips between HEALTHY and UNHEALTHY (task and first
// container) and CPU between "256" and "512". Safe while pollers run.
func (e *Estate) MutateTasks(target awsx.Target, n int) {
	e.Mu.Lock()
	defer e.Mu.Unlock()
	var candidates []int
	for i := range e.Tasks {
		t := &e.Tasks[i]
		if t.AccountID == target.AccountID && t.Region == target.Region && aws.ToString(t.Task.LastStatus) == "RUNNING" {
			candidates = append(candidates, i)
		}
	}
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	for _, idx := range candidates[:min(n, len(candidates))] {
		task := e.Tasks[idx].Task
		task.HealthStatus = flipHealth(task.HealthStatus)
		if aws.ToString(task.Cpu) == "256" {
			task.Cpu = aws.String("512")
		} else {
			task.Cpu = aws.String("256")
		}
		containers := append([]ecstypes.Container(nil), task.Containers...)
		if len(containers) > 0 {
			containers[0].HealthStatus = flipHealth(containers[0].HealthStatus)
		}
		task.Containers = containers
		task.Version++
		e.Tasks[idx].Task = task
	}
}

func flipHealth(h ecstypes.HealthStatus) ecstypes.HealthStatus {
	if h == ecstypes.HealthStatusHealthy {
		return ecstypes.HealthStatusUnhealthy
	}
	return ecstypes.HealthStatusHealthy
}
