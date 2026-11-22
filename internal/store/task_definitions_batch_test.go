package store_test

import (
	"context"
	"testing"

	"github.com/rcss-labs/harbormaster/internal/model"
)

func TestGetTaskDefinitions_ReturnsKnownARNsKeyedByARN(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	for _, arn := range []string{"arn:td-a", "arn:td-b"} {
		td := model.TaskDefinitionRef{AccountID: readAccount, Region: "eu-west-1", ARN: arn, Family: "f", Revision: 2,
			Containers: []model.ContainerDef{{Name: "app", Image: "img:" + arn}}, PolledAt: fx.now}
		if err := fx.s.UpsertTaskDefinition(ctx, td); err != nil {
			t.Fatal(err)
		}
	}
	got, err := fx.s.GetTaskDefinitions(ctx, []string{"arn:td-a", "arn:td-b", "arn:missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["arn:td-a"].Containers[0].Image != "img:arn:td-a" || got["arn:td-b"].Revision != 2 {
		t.Errorf("GetTaskDefinitions = %+v, want td-a and td-b only", got)
	}
	if empty, err := fx.s.GetTaskDefinitions(ctx, nil); err != nil || len(empty) != 0 {
		t.Errorf("GetTaskDefinitions(nil) = %v, %v", empty, err)
	}
}
