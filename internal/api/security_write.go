package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	maxAcceptanceReasonRunes = 500
	maxAcceptanceDays        = 30
)

// AcceptRiskRequest is the body of POST /api/security/images/{digest}/acceptances.
type AcceptRiskRequest struct {
	VulnID         string   `json:"vuln_id"`
	PackageName    string   `json:"package_name"`
	PackageVersion string   `json:"package_version"`
	Reason         string   `json:"reason"`
	Days           int      `json:"days"`
	ServiceIDs     []string `json:"service_ids,omitempty"`
}

// AcceptPackageRiskRequest is the body of POST /api/security/images/{digest}/package-acceptances: every finding of the
// package, in any of its versions, that a chosen service has not accepted yet.
type AcceptPackageRiskRequest struct {
	PackageName string   `json:"package_name"`
	Reason      string   `json:"reason"`
	Days        int      `json:"days"`
	ServiceIDs  []string `json:"service_ids,omitempty"`
}

// AcceptRiskResponse lists the acceptances written, one per service.
type AcceptRiskResponse struct {
	IDs []int64 `json:"ids"`
}

// imageTarget is a short audit target for a digest: "image:" and its first 12 hex digits.
func imageTarget(digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	return "image:" + hex[:min(len(hex), 12)]
}

// imageLabel is how audit text names an image: repository:tag, or the short digest when it has no tag.
func imageLabel(img store.ScanImage) string {
	if img.Tag != "" {
		return img.Repository + ":" + img.Tag
	}
	return img.Repository + "@" + strings.TrimPrefix(imageTarget(img.Digest), "image:")
}

// visibleImage loads the image named in the path for the caller. An image none of whose requesting services the caller
// can see does not exist for them: ok is false and the answer is 404.
func (s *Server) visibleImage(w http.ResponseWriter, r *http.Request, digest string) (store.ScanImage, imageView, scope, bool) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return store.ScanImage{}, imageView{}, scope{}, false
	}
	sc.Filter, sc.TagFilter, sc.Vis = store.TargetFilter{}, nil, visibilityOf(sc.Subject)
	image, err := s.Store.GetScanImage(r.Context(), digest)
	if err != nil {
		s.fail(w, err)
		return store.ScanImage{}, imageView{}, scope{}, false
	}
	views, err := s.imageViews(r, sc, []store.ScanImage{image}, nil)
	if err != nil {
		s.fail(w, err)
		return store.ScanImage{}, imageView{}, scope{}, false
	}
	if len(views) == 0 {
		s.fail(w, errNotFound)
		return store.ScanImage{}, imageView{}, scope{}, false
	}
	return image, views[0], sc, true
}

// denyAcceptRisk answers 403. The tag is named only to a caller who can see it (F38).
func (s *Server) denyAcceptRisk(w http.ResponseWriter, r *http.Request, subject authz.Subject, tagID authz.TagID) {
	if authz.Can(subject, authz.View, tagID) {
		s.denyAction(w, r, authz.AcceptRisk, tagID)
		return
	}
	writeError(w, http.StatusForbidden, "forbidden", "You need Accept risk on every service you accept this risk for.")
}

func (s *Server) postAcceptance(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	image, view, sc, ok := s.visibleImage(w, r, r.PathValue("digest"))
	if !ok {
		return
	}
	var body AcceptRiskRequest
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	reason, err := acceptanceTerms(body.Reason, body.Days)
	if err != nil {
		s.fail(w, err)
		return
	}
	findings, err := s.Store.ScanFindings(ctx, image.Digest)
	if err != nil {
		s.fail(w, err)
		return
	}
	known := false
	for _, f := range findings {
		known = known || (f.VulnID == body.VulnID && f.PackageName == body.PackageName && f.PackageVersion == body.PackageVersion)
	}
	if !known {
		s.fail(w, invalid("That finding is not in this image."))
		return
	}

	services, ok := s.acceptanceServices(w, r, sc.Subject, image.Digest, view, body.ServiceIDs)
	if !ok {
		return
	}
	now := s.now()
	acceptance := store.ScanAcceptance{
		Digest: image.Digest, VulnID: body.VulnID, PackageName: body.PackageName, PackageVersion: body.PackageVersion,
		Reason: reason, CreatedAt: now, ExpiresAt: now.Add(time.Duration(body.Days) * 24 * time.Hour),
	}
	if sc.Subject.UserID != 0 {
		acceptance.AcceptedBy = &sc.Subject.UserID
	}
	ids, err := s.Store.CreateScanAcceptances(ctx, acceptance, services)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", "That finding is already accepted for one of these services.")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}

	names := make([]string, len(services))
	serviceIDs := make([]string, len(services))
	for i, key := range services {
		names[i], serviceIDs[i] = key.Service, key.ID()
	}
	s.auditAdmin(r, "security.accept", imageTarget(image.Digest),
		fmt.Sprintf("Accepted %s in %s on %s for %s for %s.", body.VulnID, body.PackageName, imageLabel(image), dayCount(body.Days), strings.Join(names, ", ")),
		map[string]any{"digest": image.Digest, "vuln_id": body.VulnID, "package_name": body.PackageName, "package_version": body.PackageVersion,
			"days": body.Days, "reason": reason, "services": serviceIDs})
	writeJSON(w, http.StatusCreated, AcceptRiskResponse{IDs: ids})
}

// acceptanceTerms checks the reason and length of an acceptance and returns the trimmed reason.
func acceptanceTerms(reason string, days int) (string, error) {
	reason = strings.TrimSpace(reason)
	switch {
	case reason == "" || utf8.RuneCountInString(reason) > maxAcceptanceReasonRunes:
		return "", invalid(fmt.Sprintf("reason is required and at most %d characters.", maxAcceptanceReasonRunes))
	case days < 1 || days > maxAcceptanceDays:
		return "", invalid(fmt.Sprintf("days must be between 1 and %d.", maxAcceptanceDays))
	}
	return reason, nil
}

// dayCount is "1 day" or "n days".
func dayCount(days int) string {
	if days == 1 {
		return "1 day"
	}
	return strconv.Itoa(days) + " days"
}

// postPackageAcceptance accepts every finding of one package, in any version, for the chosen services. Pairs already
// accepted are left as they are; nothing left to accept is 409.
func (s *Server) postPackageAcceptance(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	image, view, sc, ok := s.visibleImage(w, r, r.PathValue("digest"))
	if !ok {
		return
	}
	var body AcceptPackageRiskRequest
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	reason, err := acceptanceTerms(body.Reason, body.Days)
	if err != nil {
		s.fail(w, err)
		return
	}
	findings, err := s.Store.ScanFindings(ctx, image.Digest)
	if err != nil {
		s.fail(w, err)
		return
	}
	var keys []store.FindingKey
	for _, f := range findings {
		if f.PackageName == body.PackageName {
			keys = append(keys, store.FindingKey{VulnID: f.VulnID, PackageName: f.PackageName, PackageVersion: f.PackageVersion})
		}
	}
	if len(keys) == 0 {
		s.fail(w, invalid("That package has no findings in this image."))
		return
	}

	services, ok := s.acceptanceServices(w, r, sc.Subject, image.Digest, view, body.ServiceIDs)
	if !ok {
		return
	}
	now := s.now()
	base := store.ScanAcceptance{Digest: image.Digest, Reason: reason, CreatedAt: now, ExpiresAt: now.Add(time.Duration(body.Days) * 24 * time.Hour)}
	if sc.Subject.UserID != 0 {
		base.AcceptedBy = &sc.Subject.UserID
	}
	ids, err := s.Store.CreatePackageScanAcceptances(ctx, base, keys, services)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", "Every finding of that package is already accepted for these services.")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}

	names := make([]string, len(services))
	serviceIDs := make([]string, len(services))
	for i, key := range services {
		names[i], serviceIDs[i] = key.Service, key.ID()
	}
	findingWord := "findings"
	if len(ids) == 1 {
		findingWord = "finding"
	}
	s.auditAdmin(r, "security.accept_package", imageTarget(image.Digest),
		fmt.Sprintf("Accepted %d %s in %s on %s for %s for %s.", len(ids), findingWord, body.PackageName, imageLabel(image), dayCount(body.Days), strings.Join(names, ", ")),
		map[string]any{"digest": image.Digest, "package_name": body.PackageName, "acceptances": len(ids),
			"days": body.Days, "reason": reason, "services": serviceIDs})
	writeJSON(w, http.StatusCreated, AcceptRiskResponse{IDs: ids})
}

// acceptanceServices picks the services an acceptance is made for and checks the caller holds Accept risk on each one's
// effective tag. Nothing is chosen unless every service passes, so a refusal writes nothing. It answers the request when
// it returns ok false.
func (s *Server) acceptanceServices(w http.ResponseWriter, r *http.Request, subject authz.Subject, digest string, view imageView, listed []string) ([]model.ServiceKey, bool) {
	tags, err := s.tags(r)
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	if len(listed) == 0 {
		var chosen []model.ServiceKey
		for _, key := range view.services {
			if authz.Can(subject, authz.AcceptRisk, authz.EffectiveServiceTag(tags, key)) {
				chosen = append(chosen, key)
			}
		}
		if len(chosen) == 0 {
			writeError(w, http.StatusForbidden, "forbidden", "You need Accept risk on a service that requested this image")
			return nil, false
		}
		return chosen, true
	}

	requesting, err := s.Store.RequestingServices(r.Context(), digest)
	if err != nil {
		s.fail(w, err)
		return nil, false
	}
	requested := map[model.ServiceKey]bool{}
	for _, key := range requesting {
		requested[key] = true
	}
	var chosen []model.ServiceKey
	seen := map[model.ServiceKey]bool{}
	for _, id := range listed {
		key, err := model.ParseServiceID(id)
		if err != nil || !requested[key] || seen[key] {
			s.fail(w, invalid("service_ids must name each service that requested this image once."))
			return nil, false
		}
		seen[key] = true
		if tagID := authz.EffectiveServiceTag(tags, key); !authz.Can(subject, authz.AcceptRisk, tagID) {
			s.denyAcceptRisk(w, r, subject, tagID)
			return nil, false
		}
		chosen = append(chosen, key)
	}
	return chosen, true
}

func (s *Server) deleteAcceptance(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, errNotFound)
		return
	}
	ctx := r.Context()
	acceptance, err := s.Store.GetScanAcceptance(ctx, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	image, _, sc, ok := s.visibleImage(w, r, acceptance.Digest)
	if !ok {
		return
	}
	tags, err := s.tags(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	tagID := authz.EffectiveServiceTag(tags, acceptance.Service)
	accepter := acceptance.AcceptedBy != nil && *acceptance.AcceptedBy == sc.Subject.UserID
	if !accepter && !authz.Can(sc.Subject, authz.AcceptRisk, tagID) {
		s.denyAcceptRisk(w, r, sc.Subject, tagID)
		return
	}
	_, err = s.Store.RevokeScanAcceptance(ctx, id, sc.Subject.UserID, s.now())
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "conflict", "That acceptance was already revoked.")
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "security.revoke", imageTarget(image.Digest),
		fmt.Sprintf("Revoked the acceptance of %s in %s on %s for %s.", acceptance.VulnID, acceptance.PackageName, imageLabel(image), acceptance.Service.Service),
		map[string]any{"digest": image.Digest, "acceptance_id": id, "vuln_id": acceptance.VulnID, "service": acceptance.Service.ID()})
	w.WriteHeader(http.StatusNoContent)
}

// queueRescan files a manual request for the image under the newest request of a service the caller may deploy, and queues
// a scan of it. An image already queued or scanning is left to that scan.
func (s *Server) queueRescan(r *http.Request, sc scope, image store.ScanImage) (store.ScanRequest, store.ScanImage, error) {
	ctx := r.Context()
	tags, err := s.tags(r)
	if err != nil {
		return store.ScanRequest{}, store.ScanImage{}, err
	}
	requests, err := s.Store.ScanRequestsForDigests(ctx, []string{image.Digest})
	if err != nil {
		return store.ScanRequest{}, store.ScanImage{}, err
	}
	var request store.ScanRequest
	for _, candidate := range requests[image.Digest] {
		if authz.Can(sc.Subject, authz.Deploy, authz.EffectiveServiceTag(tags, candidate.Key)) {
			request = candidate
			break
		}
	}
	request.ID, request.Source, request.RequestedBy = 0, "manual", nil
	if sc.Subject.UserID != 0 {
		request.RequestedBy = &sc.Subject.UserID
	}
	stored, err := s.ensureScan(ctx, resolvedImage{
		Ref: scan.ParseImageRef(image.ImageRef), Repository: image.Repository, Tag: image.Tag, Digest: image.Digest,
		MediaType: image.ManifestMediaType, Platform: image.Platform,
	}, request, ensureRescan)
	return request, stored, err
}

func (s *Server) postRescan(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	image, view, sc, ok := s.visibleImage(w, r, r.PathValue("digest"))
	if !ok {
		return
	}
	if !view.dto.Can.Rescan {
		writeError(w, http.StatusForbidden, "forbidden", "You need Deploy on a service that requested this image")
		return
	}
	if image.Status == scan.StatusQueued || image.Status == scan.StatusScanning {
		writeError(w, http.StatusConflict, "conflict", "This image is already waiting for a scan.")
		return
	}
	request, stored, err := s.queueRescan(r, sc, image)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "security.rescan", imageTarget(image.Digest), "Requested a rescan of "+imageLabel(image)+".",
		map[string]any{"digest": image.Digest, "service": request.Key.ID()})
	status, err := s.scanStatusOf(r, sc, request.Key, stored)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, status)
}

// putBaseImage sets, or with an empty ref clears, the base image of the image's repository, then queues a rescan so the
// layers are compared against it. The base is shared by every tag and digest of the repository.
func (s *Server) putBaseImage(w http.ResponseWriter, r *http.Request) {
	if !s.scanningOn() {
		s.fail(w, errScanningOff)
		return
	}
	image, view, sc, ok := s.visibleImage(w, r, r.PathValue("digest"))
	if !ok {
		return
	}
	if !view.dto.Can.SetBase {
		writeError(w, http.StatusForbidden, "forbidden", "You need Deploy on a service that requested this image")
		return
	}
	var body SetBaseImageRequest
	if err := decodeStrictJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	ref := body.BaseRef
	if ref != "" && !scan.ValidBaseRef(ref) {
		s.fail(w, invalid("base_ref is not a valid image reference."))
		return
	}
	ctx := r.Context()
	if err := s.Store.SetRepoBase(ctx, repoKeyOf(image), ref, sc.Subject.UserID, s.now()); err != nil {
		s.fail(w, err)
		return
	}
	summary := "Cleared the base image of " + image.Repository + "."
	if ref != "" {
		summary = "Set the base image of " + image.Repository + " to " + ref + "."
	}
	s.auditAdmin(r, "security.base_image", imageTarget(image.Digest), summary,
		map[string]any{"digest": image.Digest, "repository": image.Repository, "base_ref": ref})

	// An image already queued or scanning is not an error: its scan reads the base when the job is built.
	if _, image, err := s.queueRescan(r, sc, image); err != nil {
		s.fail(w, err)
		return
	} else if views, err := s.imageViews(r, sc, []store.ScanImage{image}, nil); err != nil {
		s.fail(w, err)
		return
	} else if len(views) == 0 {
		s.fail(w, errNotFound)
		return
	} else if findings, err := s.findingDTOs(r, sc, image, views[0]); err != nil {
		s.fail(w, err)
		return
	} else {
		writeJSON(w, http.StatusOK, SecurityImageDetailDTO{Enabled: true, SecurityImageDTO: views[0].dto, Findings: findings})
	}
}
