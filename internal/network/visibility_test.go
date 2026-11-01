package network

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestVisibility_ServiceTaskUsesEffectiveServiceTag(t *testing.T) {
	visible := Visibility(subjectWith(prodTag), testTags())
	payments := runningTask("core-services", "payments-api", private1a, "10.20.1.23", 1)
	worker := runningTask("background-jobs", "report-worker", private1a, "10.20.1.50", 2)
	if !visible(payments) {
		t.Error("Prod-only subject must see payments-api")
	}
	if visible(worker) {
		t.Error("Prod-only subject must not see report-worker (Dev)")
	}
}

func TestVisibility_StandaloneTaskUsesClusterTag(t *testing.T) {
	standalone := runningTask("background-jobs", "", private1a, "10.20.1.60", 1)
	if Visibility(subjectWith(prodTag), testTags())(standalone) {
		t.Error("Prod-only subject must not see a standalone task of a Dev cluster")
	}
	if !Visibility(subjectWith(devTag), testTags())(standalone) {
		t.Error("Dev subject must see a standalone task of a Dev cluster")
	}
}

func TestVisibility_AdminSeesAll(t *testing.T) {
	visible := Visibility(authz.Subject{Admin: true}, testTags())
	for _, task := range allTasks() {
		if !visible(task) {
			t.Fatalf("admin must see %s", task.ARN)
		}
	}
}

func TestFilterTasks_DropsStopped(t *testing.T) {
	running := runningTask("core-services", "payments-api", private1a, "10.20.1.23", 1)
	stopped := runningTask("core-services", "payments-api", private1a, "10.20.1.24", 2)
	stopped.LastStatus = "STOPPED"
	hidden := runningTask("background-jobs", "report-worker", private1a, "10.20.1.50", 3)

	got := FilterTasks(Visibility(subjectWith(prodTag), testTags()), []model.Task{running, stopped, hidden})
	if len(got) != 1 || got[0].ARN != running.ARN {
		t.Fatalf("want only the running visible task, got %+v", got)
	}
}
