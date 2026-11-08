package api

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/scanjob"
	"github.com/rcss-labs/harbormaster/internal/store"
)

func (s *Server) scanningOn() bool { return s.Cfg.Scan.Enabled }

// errScanningOff is the 409 for a write while image scanning is off.
var errScanningOff = &APIError{Status: http.StatusConflict, Code: "scanning_off", Message: scanningOffHint}

// imageView is an image as the caller sees it, with the services whose requests the caller may see.
type imageView struct {
	dto      SecurityImageDTO
	services []model.ServiceKey
}

// imageViews builds the DTOs of images for the caller. Only requests from services the caller can see are listed (and
// narrowed by the caller's account, region and tag filters); an image without one is left out. With only set, just
// that service's requests are listed and Open and Accepted count for that service alone.
func (s *Server) imageViews(r *http.Request, sc scope, images []store.ScanImage, only *model.ServiceKey) ([]imageView, error) {
	if len(images) == 0 {
		return nil, nil
	}
	ctx := r.Context()
	tags, err := s.tags(r)
	if err != nil {
		return nil, err
	}
	tagDTOs, err := s.tagDTOs(ctx)
	if err != nil {
		return nil, err
	}
	digests := make([]string, len(images))
	for i, img := range images {
		digests[i] = img.Digest
	}
	requests, err := s.Store.ScanRequestsForDigests(ctx, digests)
	if err != nil {
		return nil, err
	}

	views := make([]imageView, 0, len(images))
	for _, img := range images {
		view := imageView{dto: imageDTO(img)}
		view.dto.Requests = []SecurityRequestDTO{}
		seen := map[model.ServiceKey]bool{}
		for _, request := range requests[img.Digest] {
			if only != nil && request.Key != *only {
				continue
			}
			tagID := authz.EffectiveServiceTag(tags, request.Key)
			if !authz.Can(sc.Subject, authz.View, tagID) || !sc.tagMatches(tagID) ||
				(sc.Filter.AccountID != "" && sc.Filter.AccountID != request.Key.AccountID) ||
				(sc.Filter.Region != "" && sc.Filter.Region != request.Key.Region) {
				continue
			}
			_, ownTag := tags.ServiceTag(request.Key)
			canAccept := authz.Can(sc.Subject, authz.AcceptRisk, tagID)
			view.dto.Requests = append(view.dto.Requests, SecurityRequestDTO{
				ServiceID: request.Key.ID(), Service: request.Key.Service, Cluster: request.Key.Cluster,
				Container: request.Container, Tag: request.Tag, Source: request.Source, By: request.RequestedByUsername,
				At: request.RequestedAt, PermissionTag: inheritedTag(tagDTOs, tagID, !ownTag), CanAccept: canAccept,
			})
			view.dto.Can.Accept = view.dto.Can.Accept || canAccept
			view.dto.Can.Rescan = view.dto.Can.Rescan || authz.Can(sc.Subject, authz.Deploy, tagID)
			if !seen[request.Key] {
				seen[request.Key] = true
				view.services = append(view.services, request.Key)
			}
		}
		if len(view.dto.Requests) == 0 {
			continue
		}
		// Counts cover exactly the services whose requests are listed, so they agree with the acceptance flags.
		open, accepted, err := s.Store.OpenFindingCounts(ctx, []string{img.Digest}, view.services, s.now())
		if err != nil {
			return nil, err
		}
		counts := open[img.Digest]
		view.dto.Open = SeverityCountsDTO{Critical: counts.Critical, High: counts.High, Medium: counts.Medium, Low: counts.Low,
			Negligible: counts.Negligible, Unknown: counts.Unknown}
		view.dto.Accepted = accepted[img.Digest]
		views = append(views, view)
	}
	return views, nil
}

func imageDTO(img store.ScanImage) SecurityImageDTO {
	return SecurityImageDTO{
		Digest: img.Digest, Image: img.ImageRef, Repository: img.Repository, Tag: img.Tag,
		RegistryAccountID: img.RegistryAccountID, RegistryRegion: img.RegistryRegion,
		Status: img.Status, FailureCode: img.FailureCode, FailureDetail: img.FailureDetail,
		FailureAction: scan.FailureAction(img.FailureCode, img.FailureDetail, img.RegistryAccountID),
		Platform:      img.Platform, ScannedAt: img.ScannedAt, MatchedDBBuiltAt: img.MatchedDBBuiltAt,
		SyftVersion: img.SyftVersion, Packages: img.Packages, Truncated: img.Truncated,
		LastRequestedAt: img.LastRequestedAt,
	}
}

func imageDTOsOf(views []imageView) []SecurityImageDTO {
	out := make([]SecurityImageDTO, len(views))
	for i, view := range views {
		out[i] = view.dto
	}
	return out
}

// securityStatus describes the vulnerability DB and the scanner runs.
func (s *Server) securityStatus(r *http.Request) (SecurityDBDTO, SecurityScannerDTO, store.ScanDBState, error) {
	ctx := r.Context()
	state, err := s.Store.ScanDBState(ctx)
	if err != nil {
		return SecurityDBDTO{}, SecurityScannerDTO{}, state, err
	}
	summary, err := s.Store.ScanRunsSummary(ctx)
	if err != nil {
		return SecurityDBDTO{}, SecurityScannerDTO{}, state, err
	}
	queued, err := s.Store.CountQueuedScanImages(ctx)
	if err != nil {
		return SecurityDBDTO{}, SecurityScannerDTO{}, state, err
	}
	now := s.now()
	db := SecurityDBDTO{
		BuiltAt: state.BuiltAt, Freshness: string(scan.DBFreshness(state.BuiltAt, now, s.Cfg.Scan.DBMaxAge)),
		Refreshing: scanjob.RefreshInProgress(state, now), LastRefreshError: state.LastRefreshError,
	}
	scanner := SecurityScannerDTO{
		RunningRuns: summary.Running, RunningImages: summary.RunningImages, Queued: queued,
		LastSuccessAt: summary.LastSuccessAt, LastError: summary.LastError, FailingSince: summary.FailingSince,
	}
	return db, scanner, state, nil
}

func (s *Server) listSecurity(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		writeJSON(w, http.StatusOK, SecurityDTO{Hint: scanningOffHint, Note: securityNote, Images: []SecurityImageDTO{}})
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	images, err := s.Store.VisibleScanImages(ctx, sc.Filter, sc.Vis)
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.imageViews(r, sc, images, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	db, scanner, _, err := s.securityStatus(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SecurityDTO{
		Enabled: true, DB: db, Scanner: scanner, Note: securityNote, Images: imageDTOsOf(views), PolledAt: polledAt,
	})
}

func (s *Server) getSecurityImage(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		writeJSON(w, http.StatusOK, SecurityImageDetailDTO{Hint: scanningOffHint, SecurityImageDTO: SecurityImageDTO{Requests: []SecurityRequestDTO{}},
			Findings: []SecurityFindingDTO{}})
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	// The detail page is not narrowed by the list's filters: an image the caller can see by any service is theirs to open.
	sc.Filter, sc.TagFilter, sc.Vis = store.TargetFilter{}, nil, visibilityOf(sc.Subject)
	ctx := r.Context()
	image, err := s.Store.GetScanImage(ctx, r.PathValue("digest"))
	if err != nil {
		s.fail(w, err)
		return
	}
	views, err := s.imageViews(r, sc, []store.ScanImage{image}, nil)
	if err != nil {
		s.fail(w, err)
		return
	}
	if len(views) == 0 {
		s.fail(w, errNotFound) // an image the caller cannot see does not exist for them
		return
	}
	findings, err := s.findingDTOs(r, sc, image.Digest, views[0].services)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, SecurityImageDetailDTO{Enabled: true, SecurityImageDTO: views[0].dto, Findings: findings})
}

type findingKey struct{ vulnID, packageName, packageVersion string }

// findingDTOs lists an image's findings with their active acceptances. Acceptances of services the caller cannot see are
// left out, and a finding is accepted everywhere when every service the caller can see has accepted it.
func (s *Server) findingDTOs(r *http.Request, sc scope, digest string, visible []model.ServiceKey) ([]SecurityFindingDTO, error) {
	ctx := r.Context()
	findings, err := s.Store.ScanFindings(ctx, digest)
	if err != nil {
		return nil, err
	}
	acceptances, err := s.Store.ActiveScanAcceptances(ctx, digest, s.now())
	if err != nil {
		return nil, err
	}
	tags, err := s.tags(r)
	if err != nil {
		return nil, err
	}
	visibleSet := map[model.ServiceKey]bool{}
	for _, key := range visible {
		visibleSet[key] = true
	}
	byFinding := map[findingKey][]SecurityAcceptanceDTO{}
	acceptedBy := map[findingKey]map[model.ServiceKey]bool{}
	for _, a := range acceptances {
		if !visibleSet[a.Service] {
			continue
		}
		key := findingKey{a.VulnID, a.PackageName, a.PackageVersion}
		canRevoke := (a.AcceptedBy != nil && *a.AcceptedBy == sc.Subject.UserID) ||
			authz.Can(sc.Subject, authz.AcceptRisk, authz.EffectiveServiceTag(tags, a.Service))
		byFinding[key] = append(byFinding[key], SecurityAcceptanceDTO{
			ID: a.ID, ServiceID: a.Service.ID(), Service: a.Service.Service, By: a.AcceptedByUsername, Reason: a.Reason,
			CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt, CanRevoke: canRevoke,
		})
		if acceptedBy[key] == nil {
			acceptedBy[key] = map[model.ServiceKey]bool{}
		}
		acceptedBy[key][a.Service] = true
	}

	out := make([]SecurityFindingDTO, len(findings))
	for i, f := range findings {
		key := findingKey{f.VulnID, f.PackageName, f.PackageVersion}
		acceptancesOf := byFinding[key]
		if acceptancesOf == nil {
			acceptancesOf = []SecurityAcceptanceDTO{}
		}
		out[i] = SecurityFindingDTO{
			VulnID: f.VulnID, Severity: f.Severity, CVSS: f.CVSS, Package: f.PackageName, Version: f.PackageVersion,
			Type: f.PackageType, Location: f.Location, FixedVersion: f.FixedVersion, FixState: f.FixState,
			DataSource: f.DataSource, URL: f.URL, FirstSeenAt: f.FirstSeenAt, Acceptances: acceptancesOf,
			AcceptedEverywhere: len(visible) > 0 && len(acceptedBy[key]) == len(visible),
		}
	}
	return out, nil
}

// serviceImages lists the images the service requested, with counts for that service alone.
func (s *Server) serviceImages(r *http.Request, sc scope, key model.ServiceKey) ([]SecurityImageDTO, error) {
	images, err := s.Store.VisibleScanImages(r.Context(), store.TargetFilter{AccountID: key.AccountID, Region: key.Region}, sc.Vis)
	if err != nil {
		return nil, err
	}
	sc.Filter, sc.TagFilter = store.TargetFilter{}, nil
	views, err := s.imageViews(r, sc, images, &key)
	if err != nil {
		return nil, err
	}
	return imageDTOsOf(views), nil
}

func (s *Server) getSecurityService(w http.ResponseWriter, r *http.Request) {
	key, sc, row, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	if !s.scanningOn() {
		writeJSON(w, http.StatusOK, SecurityServiceDTO{Images: []SecurityImageDTO{}})
		return
	}
	dto, err := s.securityServiceDTO(r, sc, key, row)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

func (s *Server) securityServiceDTO(r *http.Request, sc scope, key model.ServiceKey, row store.ServiceRow) (SecurityServiceDTO, error) {
	on, err := s.Store.PreDeployScan(r.Context(), key)
	if err != nil {
		return SecurityServiceDTO{}, err
	}
	images, err := s.serviceImages(r, sc, key)
	if err != nil {
		return SecurityServiceDTO{}, err
	}
	return SecurityServiceDTO{
		Enabled: true, PreDeployScan: on, CanChangeSetting: authz.Can(sc.Subject, authz.Deploy, row.EffectiveTagID), Images: images,
	}, nil
}

func (s *Server) putScanSetting(w http.ResponseWriter, r *http.Request) {
	key, subject, row, ok := s.requireAction(w, r, authz.Deploy)
	if !ok {
		return
	}
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	var body SecuritySettingRequest
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.PreDeployScan == nil {
		s.fail(w, invalid("pre_deploy_scan is required."))
		return
	}
	ctx := r.Context()
	on := *body.PreDeployScan
	changed, err := s.Store.SetPreDeployScan(ctx, key, on, subject.UserID, s.now())
	if err != nil {
		s.fail(w, err)
		return
	}
	if changed {
		s.recordScanSetting(r, key, subject, on)
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	dto, err := s.securityServiceDTO(r, sc, key, row)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// recordScanSetting logs a changed setting. The change is already stored, so a failed log is reported but not an error.
func (s *Server) recordScanSetting(r *http.Request, key model.ServiceKey, subject authz.Subject, on bool) {
	state := "off"
	if on {
		state = "on"
	}
	text := "Turned pre-deploy image scanning " + state + "."
	err := s.Store.AppendOperation(r.Context(), store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &key.Service,
		ActorUserID: &subject.UserID, Source: "harbormaster", Text: text,
		Details: map[string]any{"op": preDeployScanOperation, "pre_deploy_scan": on},
	})
	if err != nil {
		s.log().Error("operation log write failed", "op", preDeployScanOperation, "error", err)
	}
	s.auditAdmin(r, "security.setting", "service:"+key.ID(), "Turned pre-deploy image scanning "+state+" for "+key.Service+".",
		map[string]any{"pre_deploy_scan": on})
}

func (s *Server) postScanRequest(w http.ResponseWriter, r *http.Request) {
	key, subject, _, ok := s.requireAction(w, r, authz.Deploy)
	if !ok {
		return
	}
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	var body ScanRequestBody
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if body.Container == "" || body.Tag == "" {
		s.fail(w, invalid("container and tag are required."))
		return
	}
	ctx := r.Context()
	current, platform, err := s.Deploy.ContainerImage(ctx, key, body.Container)
	if err != nil {
		s.failChange(w, err)
		return
	}
	// The tag of the container's image is replaced by the one asked for; a tag that cannot be part of a reference is
	// recognised by the reference no longer parsing as the registry it came from.
	requested := scan.ParseImageRef(current).ImageRef() + ":" + body.Tag
	// The tag must be a plain tag: ParseImageRef reads "x@sha256:..." as a digest reference, which would resolve by digest
	// and file the scan under a tag that does not point to it.
	if parsed := scan.ParseImageRef(requested); !requestTagPattern.MatchString(body.Tag) || parsed.Digest != "" || parsed.Tag != body.Tag {
		s.fail(w, invalid("Tag is not valid."))
		return
	}
	img, err := s.resolveImage(ctx, requested, platform)
	if err != nil {
		s.failResolve(w, err)
		return
	}
	mode := ensureManual
	if r.URL.Query().Get("rescan") == "true" {
		mode = ensureRescan
	}
	request := store.ScanRequest{Key: key, Container: body.Container, Image: img.Ref.ImageRef(), Tag: img.Tag, Source: "manual"}
	if subject.UserID != 0 {
		request.RequestedBy = &subject.UserID
	}
	stored, err := s.ensureScan(ctx, img, request, mode)
	if err != nil {
		s.fail(w, err)
		return
	}
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	status, err := s.scanStatusOf(r, sc, key, stored)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// requestTagPattern is the form of an image tag.
var requestTagPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)

// failResolve answers an error from resolveImage: an image that cannot be scanned is 422 with the reason, and an ECR
// problem is 503 with a retry hint.
func (s *Server) failResolve(w http.ResponseWriter, err error) {
	var unsupported errUnsupported
	var unresolved errResolve
	switch {
	case errors.As(err, &unsupported):
		writeError(w, http.StatusUnprocessableEntity, "invalid", unsupported.Message)
	case errors.As(err, &unresolved):
		s.log().Warn("resolve image failed", "code", unresolved.Code)
		w.Header().Set("Retry-After", scanRetryAfterSeconds)
		writeError(w, http.StatusServiceUnavailable, "ecr_unavailable", "Could not look up the image in ECR ("+unresolved.Code+"). Try again shortly.")
	default:
		s.fail(w, err)
	}
}

// scanStatusOf describes a service's scan of an image, with counts for that service alone.
func (s *Server) scanStatusOf(r *http.Request, sc scope, key model.ServiceKey, image store.ScanImage) (ScanStatusDTO, error) {
	sc.Filter, sc.TagFilter = store.TargetFilter{}, nil
	views, err := s.imageViews(r, sc, []store.ScanImage{image}, &key)
	if err != nil {
		return ScanStatusDTO{}, err
	}
	state, err := s.Store.ScanDBState(r.Context())
	if err != nil {
		return ScanStatusDTO{}, err
	}
	status := ScanStatusDTO{Enabled: true}
	status.State, status.Message = scanStateOf(image, state, s.now())
	if len(views) > 0 {
		status.Image = &views[0].dto
	}
	return status, nil
}

// scanStateOf maps a stored image to the state the UI shows, with its explanation.
func scanStateOf(image store.ScanImage, db store.ScanDBState, now time.Time) (state, message string) {
	switch image.Status {
	case scan.StatusQueued:
		switch {
		case scanjob.RefreshInProgress(db, now):
			return scanStateRefreshingDB, updatingVulnDataText
		case image.FailureCode == scan.CodeDBStale:
			return scanStateDBStale, scan.FailureAction(image.FailureCode, image.FailureDetail, image.RegistryAccountID)
		}
		return scanStateQueued, ""
	case scan.StatusScanning:
		return scanStateScanning, ""
	case scan.StatusScanned:
		return scanStateScanned, ""
	case scan.StatusFailed:
		message := scan.FailureAction(image.FailureCode, image.FailureDetail, image.RegistryAccountID)
		if message == "" {
			message = image.FailureDetail
		}
		return scanStateFailed, message
	}
	return scanStateNone, ""
}

func (s *Server) getScanStatus(w http.ResponseWriter, r *http.Request) {
	key, sc, ok := s.visibleServiceIgnoringFilters(w, r)
	if !ok {
		return
	}
	if !s.scanningOn() {
		writeJSON(w, http.StatusOK, ScanStatusDTO{State: scanStateNone, Message: scanningOffHint})
		return
	}
	query := r.URL.Query()
	container, tag, digest := query.Get("container"), query.Get("tag"), query.Get("digest")
	ctx := r.Context()

	var request store.ScanRequest
	var err error
	switch {
	case digest != "":
		request, err = s.latestRequestForDigest(r, key, container, digest)
	case container != "":
		request, err = s.Store.LatestScanRequest(ctx, key, container, tag, "")
	default:
		s.fail(w, invalid("Give container and tag, or digest."))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, ScanStatusDTO{Enabled: true, State: scanStateNone})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	image, err := s.Store.GetScanImage(ctx, request.Digest)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, ScanStatusDTO{Enabled: true, State: scanStateNone})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	status, err := s.scanStatusOf(r, sc, key, image)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// visibleServiceIgnoringFilters is visibleService for a route whose own ?tag= is an image tag, not the ?tag= filter of
// the list routes.
func (s *Server) visibleServiceIgnoringFilters(w http.ResponseWriter, r *http.Request) (model.ServiceKey, scope, bool) {
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return key, scope{}, false
	}
	subject := auth.SubjectFrom(r.Context())
	vis := visibilityOf(subject)
	if _, err := s.Store.GetServiceView(r.Context(), key, vis); err != nil {
		s.fail(w, err)
		return key, scope{}, false
	}
	return key, scope{Subject: subject, Vis: vis}, true
}

// latestRequestForDigest is the service's newest request for a digest, for the given container when one is named.
func (s *Server) latestRequestForDigest(r *http.Request, key model.ServiceKey, container, digest string) (store.ScanRequest, error) {
	requests, err := s.Store.ScanRequestsForDigests(r.Context(), []string{digest})
	if err != nil {
		return store.ScanRequest{}, err
	}
	for _, request := range requests[digest] { // newest first
		if request.Key == key && (container == "" || request.Container == container) {
			return request, nil
		}
	}
	return store.ScanRequest{}, store.ErrNotFound
}
