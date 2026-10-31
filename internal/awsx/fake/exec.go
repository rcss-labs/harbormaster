package fake

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

const execNotEnabledMessage = "The execute command failed because execute command was not enabled when the task was run or the execute command agent isn’t running. Wait and try again or run a new task with execute command enabled and try again."

var errNoExecServer = errors.New("fake: no exec server")

// ExecuteCommand starts a session on the fake SSM server for a task whose container has a running ExecuteCommandAgent.
func (c *ecsClient) ExecuteCommand(_ context.Context, in *ecs.ExecuteCommandInput, _ ...func(*ecs.Options)) (*ecs.ExecuteCommandOutput, error) {
	if err := c.f.begin("ExecuteCommand"); err != nil {
		return nil, err
	}
	if c.f.ExecServer == nil {
		return nil, errNoExecServer
	}
	if in.Command == nil || *in.Command == "" {
		return nil, invalidParameter("Command is required")
	}
	e := c.f.Estate
	e.Mu.Lock()
	cluster := clusterNameOf(in.Cluster)
	if err := c.requireCluster(cluster); err != nil {
		e.Mu.Unlock()
		return nil, err
	}
	ref := aws.ToString(in.Task)
	var task *ecstypes.Task
	for i := range e.Tasks {
		t := &e.Tasks[i]
		if c.inScope(t.AccountID, t.Region) && t.ClusterName == cluster &&
			(aws.ToString(t.Task.TaskArn) == ref || lastSegment(aws.ToString(t.Task.TaskArn)) == ref) {
			copied := t.Task
			task = &copied
			break
		}
	}
	e.Mu.Unlock()
	if task == nil {
		return nil, invalidParameter("Task not found.")
	}

	var container *ecstypes.Container
	switch {
	case in.Container != nil:
		for i := range task.Containers {
			if aws.ToString(task.Containers[i].Name) == *in.Container {
				container = &task.Containers[i]
			}
		}
	case len(task.Containers) == 1:
		container = &task.Containers[0]
	default:
		return nil, invalidParameter("The container name must be specified for a task with multiple containers.")
	}
	if container == nil {
		return nil, invalidParameter("The specified container does not exist in the task.")
	}
	if !task.EnableExecuteCommand || !agentRunning(*container) {
		return nil, invalidParameter(execNotEnabledMessage)
	}

	sessionID, streamURL, token := c.f.ExecServer.Issue(aws.ToString(task.TaskArn), aws.ToString(container.Name), *in.Command)
	return &ecs.ExecuteCommandOutput{
		ClusterArn:    task.ClusterArn,
		ContainerArn:  container.ContainerArn,
		ContainerName: container.Name,
		Interactive:   true,
		TaskArn:       task.TaskArn,
		Session:       &ecstypes.Session{SessionId: aws.String(sessionID), StreamUrl: aws.String(streamURL), TokenValue: aws.String(token)},
	}, nil
}

func agentRunning(ct ecstypes.Container) bool {
	for _, a := range ct.ManagedAgents {
		if a.Name == ecstypes.ManagedAgentNameExecuteCommandAgent && aws.ToString(a.LastStatus) == "RUNNING" {
			return true
		}
	}
	return false
}

// ssmClient is the fake SSM API; TerminateSession drops the fake agent's socket.
type ssmClient struct{ f *Fake }

var _ awsx.SSMAPI = (*ssmClient)(nil)

// TerminateSession terminates a fake exec session. An unknown session yields a nil output and no
// error, mirroring the brief for this fake.
func (c *ssmClient) TerminateSession(_ context.Context, in *ssm.TerminateSessionInput, _ ...func(*ssm.Options)) (*ssm.TerminateSessionOutput, error) {
	if err := c.f.begin("TerminateSession"); err != nil {
		return nil, err
	}
	if c.f.ExecServer == nil {
		return nil, errNoExecServer
	}
	if !c.f.ExecServer.Terminate(aws.ToString(in.SessionId)) {
		return nil, nil
	}
	return &ssm.TerminateSessionOutput{SessionId: in.SessionId}, nil
}
