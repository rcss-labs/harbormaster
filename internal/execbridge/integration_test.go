package execbridge_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"

	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/execbridge"
)

// These tests talk to real AWS. They run only when HM_IT_AWS=1 and are skipped otherwise,
// so `go test ./...` never calls AWS. See README "Terminal" for how to run them.

const integrationSkipMessage = "set HM_IT_AWS=1 and HM_IT_* to run"

// integrationSettings is read from HM_IT_* variables. Only ContainerName may be empty.
type integrationSettings struct {
	roleARN, externalID, region, accountID, cluster, taskARN, container, command string
}

// staticRoleSource returns the test role for the test account and refuses any other account.
type staticRoleSource struct {
	accountID, roleARN, externalID string
}

func (s staticRoleSource) Role(_ context.Context, accountID string) (string, string, error) {
	if accountID != s.accountID {
		return "", "", fmt.Errorf("account %s: %w", accountID, awsx.ErrAccountUnknown)
	}
	return s.roleARN, s.externalID, nil
}

// skipUnlessIntegration must run before anything else so that unset variables never touch AWS config.
func skipUnlessIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("HM_IT_AWS") != "1" {
		t.Skip(integrationSkipMessage)
	}
}

func loadIntegrationSettings(t *testing.T) integrationSettings {
	t.Helper()
	settings := integrationSettings{
		roleARN:    os.Getenv("HM_IT_ROLE_ARN"),
		externalID: os.Getenv("HM_IT_EXTERNAL_ID"),
		region:     os.Getenv("HM_IT_REGION"),
		accountID:  os.Getenv("HM_IT_ACCOUNT_ID"),
		cluster:    os.Getenv("HM_IT_CLUSTER"),
		taskARN:    os.Getenv("HM_IT_TASK_ARN"),
		container:  os.Getenv("HM_IT_CONTAINER"),
		command:    os.Getenv("HM_IT_COMMAND"),
	}
	if settings.command == "" {
		settings.command = "/bin/sh"
	}
	var missing []string
	for name, value := range map[string]string{
		"HM_IT_ROLE_ARN":   settings.roleARN,
		"HM_IT_REGION":     settings.region,
		"HM_IT_ACCOUNT_ID": settings.accountID,
		"HM_IT_CLUSTER":    settings.cluster,
		"HM_IT_TASK_ARN":   settings.taskARN,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("HM_IT_AWS=1 but these variables are unset: %s", strings.Join(missing, ", "))
	}
	return settings
}

// realClients builds the production AWS clients, assuming the test role for the test account.
func realClients(t *testing.T, settings integrationSettings) awsx.Clients {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base, err := config.LoadDefaultConfig(ctx, config.WithRegion(settings.region))
	if err != nil {
		t.Fatalf("load AWS config: %v", err)
	}
	roles := staticRoleSource{accountID: settings.accountID, roleARN: settings.roleARN, externalID: settings.externalID}
	return awsx.NewReal(base, roles)
}

// resolveContainer returns HM_IT_CONTAINER, or the first container of taskARN when it is unset.
func resolveContainer(t *testing.T, clients awsx.Clients, settings integrationSettings, taskARN string) string {
	t.Helper()
	if settings.container != "" {
		return settings.container
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	api, err := clients.ECS(ctx, awsx.Target{AccountID: settings.accountID, Region: settings.region})
	if err != nil {
		t.Fatalf("ECS client: %v", err)
	}
	out, err := api.DescribeTasks(ctx, &ecs.DescribeTasksInput{
		Cluster: aws.String(settings.cluster),
		Tasks:   []string{taskARN},
	})
	if err != nil {
		t.Fatalf("DescribeTasks: %v", err)
	}
	if len(out.Tasks) == 0 || len(out.Tasks[0].Containers) == 0 {
		t.Fatalf("task %s has no containers (failures: %+v)", taskARN, out.Failures)
	}
	return aws.ToString(out.Tasks[0].Containers[0].Name)
}

func TestIntegration_ExecShellRoundTrip(t *testing.T) {
	skipUnlessIntegration(t)
	settings := loadIntegrationSettings(t)
	clients := realClients(t, settings)
	container := resolveContainer(t, clients, settings, settings.taskARN)

	openCtx, cancelOpen := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelOpen()
	sess, err := execbridge.Open(openCtx, execbridge.Options{Clients: clients}, execbridge.Target{
		AccountID: settings.accountID,
		Region:    settings.region,
		Cluster:   settings.cluster,
		TaskARN:   settings.taskARN,
		Container: container,
		Command:   settings.command,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { sess.Close(execbridge.CloseByClient) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The shell expands $$ to its PID, so only the echoed command contains the literal "hm-$$-ok".
	// Matching digits between hm- and -ok proves the shell ran the command.
	if err := sess.Write(ctx, []byte("echo hm-$$-ok\r")); err != nil {
		t.Fatalf("Write echo: %v", err)
	}
	var output bytes.Buffer
	awaitOutput(t, sess.Events(), &output, regexp.MustCompile(`hm-[0-9]+-ok`), 10*time.Second)

	if err := sess.Write(ctx, []byte("exit\r")); err != nil {
		t.Fatalf("Write exit: %v", err)
	}
	reason := awaitClosed(t, sess.Events(), 10*time.Second)
	if reason != execbridge.CloseByAgent {
		t.Fatalf("close reason = %q, want %q", reason, execbridge.CloseByAgent)
	}
}

func TestIntegration_ExecCommandRejectedWhenExecOff(t *testing.T) {
	skipUnlessIntegration(t)
	noExecTask := os.Getenv("HM_IT_TASK_ARN_NOEXEC")
	if noExecTask == "" {
		t.Skip("set HM_IT_TASK_ARN_NOEXEC to a task whose service has no enable_execute_command to run")
	}
	settings := loadIntegrationSettings(t)
	clients := realClients(t, settings)
	container := resolveContainer(t, clients, settings, noExecTask)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sess, err := execbridge.Open(ctx, execbridge.Options{Clients: clients}, execbridge.Target{
		AccountID: settings.accountID,
		Region:    settings.region,
		Cluster:   settings.cluster,
		TaskARN:   noExecTask,
		Container: container,
		Command:   settings.command,
	})
	if err == nil {
		sess.Close(execbridge.CloseByClient)
		t.Fatal("Open succeeded on a task without exec enabled, want an error")
	}
	var openErr *execbridge.OpenError
	if !errors.As(err, &openErr) || openErr.Stage != "execute_command" {
		t.Fatalf("Open error = %v, want *OpenError with stage execute_command", err)
	}
}

// awaitOutput collects output until it matches want, failing if the session closes or the timeout passes.
func awaitOutput(t *testing.T, events <-chan execbridge.Event, output *bytes.Buffer, want *regexp.Regexp, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for !want.Match(output.Bytes()) {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatalf("event stream closed before output matched %s; got %q", want, output.String())
			}
			switch ev.Kind {
			case execbridge.EventOutput:
				output.Write(ev.Data)
			case execbridge.EventClosed:
				t.Fatalf("session closed (%s) before output matched %s; got %q", ev.Reason, want, output.String())
			}
		case <-deadline:
			t.Fatalf("no output matching %s within %s; got %q", want, timeout, output.String())
		}
	}
}

// awaitClosed drains events until the session reports EventClosed and returns its reason.
func awaitClosed(t *testing.T, events <-chan execbridge.Event, timeout time.Duration) execbridge.CloseReason {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event stream closed without an EventClosed event")
			}
			if ev.Kind == execbridge.EventClosed {
				return ev.Reason
			}
		case <-deadline:
			t.Fatalf("session did not close within %s", timeout)
		}
	}
}
