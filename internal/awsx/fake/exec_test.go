package fake

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/rcss-labs/harbormaster/internal/awsx"
)

var homelabEast = awsx.Target{AccountID: homelabID, Region: "us-east-1"}

// taskOfService returns the cluster, region and ARN of the idx-th RUNNING task of a service in the homelab account.
func taskOfService(t *testing.T, f *Fake, service string, idx int) (cluster, region, arn string) {
	t.Helper()
	f.Estate.Mu.Lock()
	defer f.Estate.Mu.Unlock()
	n := 0
	for _, task := range f.Estate.Tasks {
		if task.AccountID == homelabID && aws.ToString(task.Task.Group) == "service:"+service && aws.ToString(task.Task.LastStatus) == "RUNNING" {
			if n == idx {
				return task.ClusterName, task.Region, aws.ToString(task.Task.TaskArn)
			}
			n++
		}
	}
	t.Fatalf("no running task %d for %s", idx, service)
	return "", "", ""
}

func execFake(t *testing.T) *Fake {
	t.Helper()
	f := newFake()
	f.ExecServer = NewSSMServer(SSMServerOptions{})
	f.ExecServer.Start()
	t.Cleanup(f.ExecServer.Close)
	return f
}

func executeCommand(f *Fake, t *testing.T, cluster, region, arn, container string) (*ecs.ExecuteCommandOutput, error) {
	t.Helper()
	api := ecsFor(t, f, homelabID, region)
	return api.ExecuteCommand(context.Background(), &ecs.ExecuteCommandInput{
		Cluster: aws.String(cluster), Task: aws.String(arn), Container: aws.String(container),
		Command: aws.String("/bin/sh"), Interactive: true,
	})
}

func TestFakeECS_ExecuteCommand_ReturnsSessionForExecEnabledTask(t *testing.T) {
	f := execFake(t)
	cluster, region, arn := taskOfService(t, f, "payments-api", 0)
	out, err := executeCommand(f, t, cluster, region, arn, "payments-api")
	if err != nil {
		t.Fatalf("ExecuteCommand: %v", err)
	}
	base := f.ExecServer.baseURL()
	if out.Session == nil || !strings.HasPrefix(aws.ToString(out.Session.StreamUrl), base+"/v1/data-channel/") ||
		aws.ToString(out.Session.TokenValue) == "" || !strings.HasPrefix(aws.ToString(out.Session.SessionId), "ecs-execute-command-") {
		t.Fatalf("session = %+v, want stream URL under %s, token and id", out.Session, base)
	}
	if !out.Interactive || aws.ToString(out.TaskArn) != arn || aws.ToString(out.ContainerName) != "payments-api" {
		t.Fatalf("output = %+v, want interactive for the requested task and container", out)
	}
	if f.Calls()["ExecuteCommand"] != 1 {
		t.Fatalf("calls = %v, want ExecuteCommand counted", f.Calls())
	}
}

func TestFakeECS_ExecuteCommand_FailsWhenExecOff(t *testing.T) {
	f := execFake(t)
	cluster, region, arn := taskOfService(t, f, "cert-renewer", 0)
	_, err := executeCommand(f, t, cluster, region, arn, "cert-renewer")
	if err == nil || !strings.Contains(err.Error(), "execute command was not enabled") {
		t.Fatalf("err = %v, want execute-command-not-enabled", err)
	}
}

func TestFakeECS_ExecuteCommand_FailsWhenAgentNotRunning(t *testing.T) {
	f := execFake(t)
	var pending int
	for i := 0; i < 2; i++ {
		cluster, region, arn := taskOfService(t, f, "auth-service", i)
		_, err := executeCommand(f, t, cluster, region, arn, "auth-service")
		if err != nil {
			if !strings.Contains(err.Error(), "execute command was not enabled") {
				t.Fatalf("err = %v, want execute-command-not-enabled", err)
			}
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("auth-service tasks with a non-running agent = %d, want exactly 1", pending)
	}
}

func TestFakeECS_ExecuteCommand_NoExecServer(t *testing.T) {
	f := newFake()
	cluster, region, arn := taskOfService(t, f, "payments-api", 0)
	if _, err := executeCommand(f, t, cluster, region, arn, "payments-api"); err == nil || !strings.Contains(err.Error(), "fake: no exec server") {
		t.Fatalf("err = %v, want fake: no exec server", err)
	}
}

func TestFakeSSM_TerminateSession_MarksTerminated(t *testing.T) {
	f := execFake(t)
	id, _, _ := f.ExecServer.Issue("arn", "c", "/bin/sh")
	api, err := f.SSM(context.Background(), homelabEast)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.TerminateSession(context.Background(), &ssm.TerminateSessionInput{SessionId: aws.String(id)}); err != nil {
		t.Fatalf("TerminateSession: %v", err)
	}
	if ss, ok := f.ExecServer.Session(id); !ok || !ss.Terminated {
		t.Fatalf("session = %+v ok=%v, want terminated", ss, ok)
	}
	out, err := api.TerminateSession(context.Background(), &ssm.TerminateSessionInput{SessionId: aws.String("ecs-execute-command-unknown")})
	if out != nil || err != nil {
		t.Fatalf("unknown session = %v, %v; want nil, nil", out, err)
	}
	if f.Calls()["TerminateSession"] != 2 {
		t.Fatalf("calls = %v, want TerminateSession counted twice", f.Calls())
	}
}
