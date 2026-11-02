package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/rcss-labs/harbormaster/internal/model"
)

// execer is what the shared write statements need; both the pool and a transaction satisfy it.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Tx is the set of writes that can run inside one transaction (see InTx).
// Each method is the same statement as the Store method of the same name.
type Tx struct{ tx pgx.Tx }

// InTx runs fn in one transaction: it commits when fn returns nil and rolls back otherwise.
// The post-AWS writes of each action (register, rollout, outcome) use it so a half-recorded
// change never exists.
func (s *Store) InTx(ctx context.Context, fn func(Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return fn(Tx{tx: tx}) })
}

func (t Tx) UpsertTaskDefinition(ctx context.Context, td model.TaskDefinitionRef) error {
	return upsertTaskDefinition(ctx, t.tx, td)
}

func (t Tx) RecordRevision(ctx context.Context, taskDefinitionARN string, userID int64, at time.Time) error {
	return recordRevision(ctx, t.tx, taskDefinitionARN, userID, at)
}

func (t Tx) AppendOperation(ctx context.Context, op Operation) error {
	return appendOperation(ctx, t.tx, op)
}

func (t Tx) AttributeDeployment(ctx context.Context, d AttributedDeployment) error {
	return attributeDeployment(ctx, t.tx, d)
}

func (t Tx) FinishDeployment(ctx context.Context, k model.ServiceKey, deploymentID, outcome string, finishedAt time.Time, failure *FailureInfo) (bool, error) {
	return finishDeployment(ctx, t.tx, k, deploymentID, outcome, finishedAt, failure)
}
