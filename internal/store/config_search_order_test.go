package store_test

import (
	"context"
	"testing"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// A running ACTIVE deployment stored without a Revision must never sort ahead of numbered revisions.
func TestConfigSearchRows_NullRevisionSortsLast(t *testing.T) {
	fx := newReadFixture(t)
	ctx := context.Background()
	def := sampleDefinition()
	primary := putConfigTaskDefinition(t, fx, "notifications", 1, def)
	numbered := putConfigTaskDefinition(t, fx, "notifications", 2, def)
	unnumbered := putConfigTaskDefinition(t, fx, "notifications", 3, ecstypes.TaskDefinition{})
	setConfigService(t, fx, "core-services", "notifications", "notifications", 10, primary, nil)
	_, err := fx.s.Pool.Exec(ctx, `update services set deployments = $1::jsonb where service_name = 'notifications'`,
		`[{"Status":"ACTIVE","TaskDefinitionARN":"`+unnumbered+`","Running":1},
		  {"Status":"ACTIVE","TaskDefinitionARN":"`+numbered+`","Revision":9,"Running":1}]`)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := fx.s.ConfigSearchRows(ctx, store.TargetFilter{}, store.Visibility{Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	revs := configRowFor(t, rows, "notifications").Revisions
	if len(revs) != 3 || revs[1].ARN != numbered || revs[2].ARN != unnumbered {
		t.Errorf("revisions = %+v, want primary, the numbered :9, then the unnumbered one", revs)
	}
}
