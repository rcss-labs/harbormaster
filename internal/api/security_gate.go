package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// The gate stands between a changed image and the only deploy path. It never answers an HTTP error for a scanner, S3, ECR
// or leader problem: each is a row the caller may acknowledge with "Deploy anyway". The server re-evaluates it on every
// call, so a client cannot prove it passed earlier and a direct API call cannot skip it.

// Gate states beyond the scan states of security_dto.go.
const (
	gateStatePass     = "pass"
	gateStateCritical = "critical"

	deployAnywayOperation = "deploy_anyway"
	gateTimeoutMessage    = "The scan did not finish in time."
	// scanRunGrace is how long past HM_SCAN_TASK_TIMEOUT a run may still be scanning before the gate stops waiting for
	// the leader to time it out.
	scanRunGrace = 5 * time.Minute
)

// ScanGateAck is the caller's "Deploy anyway" for one image: the container and the ref its gate row showed, which is the
// digest resolved by that call, or the image reference when none resolved.
type ScanGateAck struct {
	Container string `json:"container"`
	Ref       string `json:"ref"`
}

// ScanGateRequest is the scan_gate member of a change set or registration.
type ScanGateRequest struct {
	DeployAnyway bool          `json:"deploy_anyway"`
	Images       []ScanGateAck `json:"images"`
}

// ScanGateImageDTO is the gate's verdict on one changed image. Ref is the digest resolved in this call, or the image
// reference when none resolved; Open and Accepted count for the deploying service alone.
type ScanGateImageDTO struct {
	Container string            `json:"container"`
	Image     string            `json:"image"`
	Ref       string            `json:"ref"`
	Digest    string            `json:"digest"`
	State     string            `json:"state"`
	Open      SeverityCountsDTO `json:"open"`
	Accepted  int               `json:"accepted"`
	Message   string            `json:"message"`
}

// ScanGateErrorDTO is the scan_gate member of the 409 answer.
type ScanGateErrorDTO struct {
	Message string             `json:"message"`
	Images  []ScanGateImageDTO `json:"images"`
}

// gateOutcome is the gate's decision. Acknowledged lists the rows that did not pass and were let through by the caller's
// "Deploy anyway"; it is what gets recorded once the apply has succeeded.
type gateOutcome struct {
	Passed       bool
	Gate         ScanGateErrorDTO
	Acknowledged []ScanGateImageDTO
}

// scanGate judges the images a submitted definition changes. It is skipped (Passed) when scanning is off, the service's
// setting is off, or the definition cannot be read (the apply answers 422 for that). A changed image is one whose
// container name is new or whose image string differs from the definition of baseRevision, which the apply refuses
// unless it is the live revision. An error is returned only for Harbormaster's own database.
func (s *Server) scanGate(ctx context.Context, key model.ServiceKey, baseRevision int, definition map[string]any, ack *ScanGateRequest, actor authz.Subject) (gateOutcome, error) {
	passed := gateOutcome{Passed: true}
	if !s.scanningOn() || definition == nil {
		return passed, nil
	}
	on, err := s.Store.PreDeployScan(ctx, key)
	if err != nil {
		return gateOutcome{}, err
	}
	if !on {
		return passed, nil
	}
	submitted, platform, err := deploy.DefinitionImages(definition)
	if err != nil {
		return passed, nil
	}
	baseline := s.baselineImages(ctx, key, baseRevision)

	containers := make([]string, 0, len(submitted))
	for name, image := range submitted {
		if name == "" || image == "" {
			continue // Prepare refuses a container without a name or an image; the apply answers 422
		}
		if previous, known := baseline[name]; !known || previous != image {
			containers = append(containers, name)
		}
	}
	slices.Sort(containers)
	rows := make([]ScanGateImageDTO, 0, len(containers))
	for _, name := range containers {
		row, err := s.gateRow(ctx, key, name, submitted[name], platform, actor)
		if err != nil {
			return gateOutcome{}, err
		}
		rows = append(rows, row)
	}

	var needAck []ScanGateImageDTO
	for _, row := range rows {
		if row.State != gateStatePass {
			needAck = append(needAck, row)
		}
	}
	if len(needAck) == 0 {
		return passed, nil
	}
	if ack != nil && ack.DeployAnyway && allAcknowledged(needAck, ack.Images) {
		return gateOutcome{Passed: true, Acknowledged: needAck}, nil
	}
	return gateOutcome{Gate: ScanGateErrorDTO{
		Message: "Some images have not passed the pre-deploy scan. You can deploy anyway.", Images: rows,
	}}, nil
}

// allAcknowledged reports whether every row has an acknowledgement for the same container and ref.
func allAcknowledged(rows []ScanGateImageDTO, acks []ScanGateAck) bool {
	for _, row := range rows {
		if !slices.Contains(acks, ScanGateAck{Container: row.Container, Ref: row.Ref}) {
			return false
		}
	}
	return true
}

// baselineImages is the images of the definition the request was edited from. When it cannot be read every container
// counts as changed: the gate then pauses where it could have passed, and never the other way round.
func (s *Server) baselineImages(ctx context.Context, key model.ServiceKey, baseRevision int) map[string]string {
	revision, err := s.Deploy.RevisionDefinition(ctx, key, baseRevision)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log().Warn("gate: base revision unreadable, gating every image", "service", key.ID(), "revision", baseRevision, "error", err)
		}
		return nil
	}
	images, _, err := deploy.DefinitionImages(revision.Definition)
	if err != nil {
		s.log().Warn("gate: base revision undecodable, gating every image", "service", key.ID(), "revision", baseRevision, "error", err)
		return nil
	}
	return images
}

// gateRow resolves one changed image now and judges the digest it resolves to (D22). Every image that resolves records a
// pre_deploy request for the service and container, whether its scan is queued, joined or reused.
func (s *Server) gateRow(ctx context.Context, key model.ServiceKey, container, imageRef, platform string, actor authz.Subject) (ScanGateImageDTO, error) {
	row := ScanGateImageDTO{Container: container, Image: imageRef, Ref: imageRef}
	resolved, err := s.resolveImage(ctx, imageRef, platform)
	var unsupported errUnsupported
	var unresolved errResolve
	switch {
	case errors.As(err, &unsupported):
		row.State, row.Message = scanStateUnsupported, unsupported.Message
		return row, nil
	case errors.As(err, &unresolved):
		s.log().Warn("gate: image not resolved", "service", key.ID(), "container", container, "code", unresolved.Code)
		row.State, row.Message = scanStateFailed, "The image could not be checked in ECR ("+unresolved.Code+"). You can deploy anyway."
		return row, nil
	case err != nil:
		return ScanGateImageDTO{}, err
	}
	row.Digest, row.Ref = resolved.Digest, resolved.Digest

	request := store.ScanRequest{Key: key, Container: container, Image: resolved.Ref.ImageRef(), Tag: resolved.Tag, Source: "pre_deploy"}
	if actor.UserID != 0 {
		request.RequestedBy = &actor.UserID
	}
	image, err := s.ensureScan(ctx, resolved, request, ensureGate)
	if err != nil {
		return ScanGateImageDTO{}, err
	}
	now := s.now()
	row.State, row.Message, err = s.gateState(ctx, image, now)
	if err != nil {
		return ScanGateImageDTO{}, err
	}
	if image.Status != scan.StatusScanned || row.State == scanStateFailed {
		return row, nil
	}
	open, accepted, err := s.Store.OpenFindingCounts(ctx, []string{image.Digest}, []model.ServiceKey{key}, now)
	if err != nil {
		return ScanGateImageDTO{}, err
	}
	counts := open[image.Digest]
	row.Open = SeverityCountsDTO{Critical: counts.Critical, High: counts.High, Medium: counts.Medium, Low: counts.Low,
		Negligible: counts.Negligible, Unknown: counts.Unknown}
	row.Accepted = accepted[image.Digest]
	if counts.Critical > 0 {
		row.State = gateStateCritical
	}
	return row, nil
}

// gateState maps a stored image to its gate state, in the order of D17: a failed scan stays failed (the gate never
// requeues it); a queued or scanning one that has outlived its time is released as failed, so a dead leader or scanner
// cannot hold a deploy; a scanned one is pass here and judged for criticals by the caller.
func (s *Server) gateState(ctx context.Context, image store.ScanImage, now time.Time) (state, message string, err error) {
	timeout := s.Cfg.Scan.TaskTimeout
	switch image.Status {
	case scan.StatusFailed:
		if image.FailureCode == scan.CodeTimeout {
			return scanStateFailed, gateTimeoutMessage, nil
		}
		message = scan.FailureAction(image.FailureCode, image.FailureDetail, image.RegistryAccountID)
		if message == "" {
			message = image.FailureDetail
		}
		if message == "" {
			message = "The scan failed."
		}
		return scanStateFailed, message, nil
	case scan.StatusQueued:
		if image.FailureCode == scan.CodeDBStale {
			return scanStateDBStale, scan.FailureAction(image.FailureCode, image.FailureDetail, image.RegistryAccountID), nil
		}
		retryPending := image.NextAttemptAt != nil && image.NextAttemptAt.After(now)
		waitedTooLong := image.QueuedAt != nil && now.Sub(*image.QueuedAt) > timeout
		if retryPending || waitedTooLong {
			return scanStateFailed, gateTimeoutMessage, nil
		}
		dbState, err := s.Store.ScanDBState(ctx)
		if err != nil {
			return "", "", err
		}
		if scanjob.RefreshInProgress(dbState, now) {
			return scanStateRefreshingDB, updatingVulnDataText, nil
		}
		return scanStateQueued, "", nil
	case scan.StatusScanning:
		started, err := s.Store.ActiveScanRunStart(ctx, image.Digest)
		if err != nil {
			return "", "", err
		}
		if started == nil || now.Sub(*started) > timeout+scanRunGrace {
			return scanStateFailed, gateTimeoutMessage, nil
		}
		return scanStateScanning, "", nil
	}
	return gateStatePass, "", nil
}

// writeScanGate answers the pause. The top-level code is scan_gate, never conflict: the Task definition tab reads
// conflict as a moved revision.
func writeScanGate(w http.ResponseWriter, gate ScanGateErrorDTO) {
	writeJSON(w, http.StatusConflict, map[string]any{
		"error":     map[string]string{"code": "scan_gate", "message": gate.Message},
		"scan_gate": gate,
	})
}

// checkScanGate runs the gate for a request and answers it when the request must stop; ok is false then.
func (s *Server) checkScanGate(w http.ResponseWriter, r *http.Request, key model.ServiceKey, baseRevision int, definition map[string]any, ack *ScanGateRequest, actor authz.Subject) (gateOutcome, bool) {
	outcome, err := s.scanGate(r.Context(), key, baseRevision, definition, ack, actor)
	if err != nil {
		s.fail(w, err)
		return outcome, false
	}
	if !outcome.Passed {
		writeScanGate(w, outcome.Gate)
		return outcome, false
	}
	return outcome, true
}

// recordDeployAnyway logs who let unscanned or failing images through. It runs only after the apply or registration
// returned without error, so a stale base, a validation error or a running rollout writes nothing. registeredOnly is
// true when nothing was deployed. It is detached from the request's cancellation: the change is already made.
func (s *Server) recordDeployAnyway(r *http.Request, key model.ServiceKey, outcome gateOutcome, registeredOnly bool, actor authz.Subject) error {
	ctx := context.WithoutCancel(r.Context())
	verb := "Deployed anyway"
	if registeredOnly {
		verb = "Registered anyway"
	}
	parts := make([]string, len(outcome.Acknowledged))
	for i, row := range outcome.Acknowledged {
		parts[i] = fmt.Sprintf("%s %s (%s)", row.Container, row.Image, row.State)
	}
	listed := strings.Join(parts, "; ")
	details := map[string]any{"op": deployAnywayOperation, "registered_only": registeredOnly, "images": outcome.Acknowledged}

	opErr := s.Store.AppendOperation(ctx, store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
		ActorUserID: &actor.UserID, Source: "harbormaster", Text: verb + ": " + listed + ".", Details: details,
	})
	auditErr := s.Store.Audit(ctx, store.AuditEntry{
		ActorUserID: &actor.UserID, ActorUsername: actor.Username, Action: "security.deploy_anyway", Target: "service:" + key.ID(),
		Summary: fmt.Sprintf("%s on %s: %s.", verb, key.Service, listed), Details: details, IP: s.clientIP(r),
	})
	if err := errors.Join(opErr, auditErr); err != nil {
		s.log().Error("deploy anyway not recorded", "service", key.ID(), "error", err)
		return err
	}
	return nil
}

// joinWarning adds a note to an existing warning.
func joinWarning(existing *string, note string) *string {
	if existing == nil || *existing == "" {
		return &note
	}
	joined := *existing + " " + note
	return &joined
}

const deployAnywayNotRecorded = "Deployed anyway, but Harbormaster could not record who chose it."
