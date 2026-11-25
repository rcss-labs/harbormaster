package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// investigationListLimit caps the investigations listed for one service.
const investigationListLimit = 50

// knownInvestigationState is the closed set of states this code renders. Any other state is answered as not found
// for everyone, admins included; an unknown kind is refused by the default of mayRead's switch.
func knownInvestigationState(state string) bool {
	switch state {
	case store.InvestigationRequested, store.InvestigationRunning, store.InvestigationDone, store.InvestigationFailed, store.InvestigationSkipped:
		return true
	}
	return false
}

// investigationReader is what the access rules need for one request: who asks, the current tags, and which services
// still exist. It is built for each request and never kept.
type investigationReader struct {
	subject  authz.Subject
	tags     authz.Tags
	existing map[model.ServiceKey]bool
}

// canView reports whether the reader may View the service as it is now. A service that is gone is for admins only.
func (rd investigationReader) canView(key model.ServiceKey) bool {
	if !rd.existing[key] {
		return rd.subject.Admin
	}
	return authz.Can(rd.subject, authz.View, authz.EffectiveServiceTag(rd.tags, key))
}

// mayRead applies the read rule of the investigation's kind and state. It is the one place that refuses an unknown
// state or kind, for admins too.
func (rd investigationReader) mayRead(v store.Investigation) bool {
	if !knownInvestigationState(v.State) {
		return false
	}
	switch v.SubjectKind {
	case "service":
		return rd.canView(investigationServiceKey(v))
	case "cluster", "account":
		if rd.subject.Admin {
			return true
		}
		// A list at the cap may have been cut, so it is not known to be complete.
		if len(v.Members) == 0 || len(v.Members) >= store.MaxInvestigationMembers {
			return false
		}
		for _, m := range v.Members {
			if !rd.canView(investigationMemberKey(v, m)) {
				return false
			}
		}
		return true
	}
	return false
}

// mayReadEvidence applies the rule of one item on its own: a service that is gone is for admins only, whatever the
// action; then the reader needs View on the service's current tag, and Logs as well for a logs item. Any other action
// is for nobody.
func (rd investigationReader) mayReadEvidence(e store.InvestigationEvidence) bool {
	key := evidenceKey(e)
	if !rd.canView(key) {
		return false
	}
	switch e.Action {
	case "view":
		return true
	case "logs":
		return authz.Can(rd.subject, authz.Logs, authz.EffectiveServiceTag(rd.tags, key))
	}
	return false
}

// newInvestigationReader loads the tags once per request; services are added with know.
func (s *Server) newInvestigationReader(r *http.Request) (investigationReader, error) {
	tags, err := s.tags(r)
	if err != nil {
		return investigationReader{}, err
	}
	return investigationReader{subject: auth.SubjectFrom(r.Context()), tags: tags, existing: map[model.ServiceKey]bool{}}, nil
}

// know records which of the services exist, in one query.
func (s *Server) know(r *http.Request, rd investigationReader, keys []model.ServiceKey) error {
	existing, err := s.Store.ExistingServices(r.Context(), keys)
	if err != nil {
		return err
	}
	for key := range existing {
		rd.existing[key] = true
	}
	return nil
}

func evidenceKey(e store.InvestigationEvidence) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: e.AccountID, Region: e.Region, Cluster: e.ClusterName}, Service: e.ServiceName}
}

// canonicalID parses a decimal id written without a sign, leading zeros or spaces; anything else is not an id.
func canonicalID(raw string) (int64, bool) {
	if raw == "" || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	return id, err == nil
}

// investigationDetail answers for one investigation: not found unless this reader may read it, else the summary and
// the evidence this reader may see. Everything is computed from the current permissions on every request.
func (s *Server) investigationDetail(w http.ResponseWriter, r *http.Request, v store.Investigation) {
	reader, err := s.newInvestigationReader(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	subjectKeys := make([]model.ServiceKey, 0, len(v.Members)+1)
	if v.SubjectKind == "service" {
		subjectKeys = append(subjectKeys, investigationServiceKey(v))
	}
	for _, m := range v.Members {
		subjectKeys = append(subjectKeys, investigationMemberKey(v, m))
	}
	if err := s.know(r, reader, subjectKeys); err != nil {
		s.fail(w, err)
		return
	}
	if !reader.mayRead(v) {
		s.fail(w, store.ErrNotFound)
		return
	}
	// Evidence is loaded only for a reader who may read the investigation at all.
	evidence, err := s.Store.ListInvestigationEvidence(r.Context(), v.ID)
	if err != nil {
		s.fail(w, err)
		return
	}
	evidenceKeys := make([]model.ServiceKey, 0, len(evidence))
	for _, e := range evidence {
		evidenceKeys = append(evidenceKeys, evidenceKey(e))
	}
	if err := s.know(r, reader, evidenceKeys); err != nil {
		s.fail(w, err)
		return
	}
	out := InvestigationDetailDTO{
		InvestigationDTO: investigationDTO(v),
		Members:          make([]InvestigationMemberDTO, 0, len(v.Members)),
		Evidence:         make([]InvestigationEvidenceDTO, 0, len(evidence)),
	}
	for _, m := range v.Members {
		out.Members = append(out.Members, InvestigationMemberDTO{ServiceID: investigationMemberID(v, m), Service: m.Service, Cluster: m.Cluster})
	}
	for _, e := range evidence {
		if !reader.mayReadEvidence(e) {
			out.EvidenceHidden++
			continue
		}
		out.Evidence = append(out.Evidence, InvestigationEvidenceDTO{
			Ordinal: e.Ordinal, Tool: e.Tool, Action: e.Action, ServiceID: evidenceKey(e).ID(), Service: e.ServiceName,
			Note: e.Note, Excerpt: e.Excerpt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// getInvestigation answers GET /api/investigations/{id}.
func (s *Server) getInvestigation(w http.ResponseWriter, r *http.Request) {
	id, ok := canonicalID(r.PathValue("id"))
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	v, err := s.Store.GetInvestigation(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.investigationDetail(w, r, v)
}

// getIncidentInvestigation answers GET /api/investigations/by-incident/{incidentId}.
func (s *Server) getIncidentInvestigation(w http.ResponseWriter, r *http.Request) {
	incident, ok := canonicalID(r.PathValue("incidentId"))
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	v, err := s.Store.GetInvestigationByIncident(r.Context(), incident)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.investigationDetail(w, r, v)
}

// listServiceInvestigations answers GET /api/services/{serviceId}/investigations: the newest 50 of a service the
// caller can View. Its summaries are for everyone who sees the subject, so no evidence is looked at here.
func (s *Server) listServiceInvestigations(w http.ResponseWriter, r *http.Request) {
	key, _, _, ok := s.visibleService(w, r)
	if !ok {
		return
	}
	rows, err := s.Store.ListServiceInvestigations(r.Context(), key, investigationListLimit)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := ItemsResponse[InvestigationDTO]{Items: make([]InvestigationDTO, 0, len(rows))}
	for _, v := range rows {
		if knownInvestigationState(v.State) {
			out.Items = append(out.Items, investigationDTO(v))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// alertInvestigationLinks finds the open investigation of each service alert, or returns nil when the alert body
// carries no such member: the agent is off, or the caller authenticated with an access token.
func (s *Server) alertInvestigationLinks(r *http.Request, alerts []model.Alert) (map[store.InvestigationKey]store.Investigation, error) {
	if !s.Cfg.AgentEnabled() || auth.SubjectFrom(r.Context()).ReadOnly {
		return nil, nil
	}
	keys := make([]model.ServiceKey, 0, len(alerts))
	for _, alert := range alerts {
		if alert.Key.Service != "" {
			keys = append(keys, alert.Key)
		}
	}
	links, err := s.Store.InvestigationsForOpenIncidents(r.Context(), keys)
	if err != nil {
		return nil, err
	}
	if links == nil {
		links = map[store.InvestigationKey]store.Investigation{}
	}
	return links, nil
}

// alertInvestigation is the JSON member of one alert: the link, or null.
func alertInvestigation(links map[store.InvestigationKey]store.Investigation, alert model.Alert) json.RawMessage {
	v, found := links[store.InvestigationKey{ServiceKey: alert.Key, Kind: alert.Kind}]
	if !found || !knownInvestigationState(v.State) {
		return json.RawMessage("null")
	}
	raw, err := json.Marshal(AlertInvestigationDTO{ID: v.ID, State: v.State, Reason: v.Reason})
	if err != nil {
		return json.RawMessage("null")
	}
	return raw
}
