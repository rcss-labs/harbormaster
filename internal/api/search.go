package api

import (
	"net/http"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/model"
)

const (
	searchMinLength = 2
	searchMaxItems  = 8
)

// search answers GET /api/search?q= over the clusters, services and running tasks the caller may see.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	items := []SearchItemDTO{}
	if len([]rune(q)) < searchMinLength {
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	hits, err := s.Store.Search(r.Context(), sc.Filter, sc.Vis, q, searchMaxItems)
	if err != nil {
		s.fail(w, err)
		return
	}
	for _, hit := range hits {
		item := SearchItemDTO{Kind: hit.Kind, Label: hit.Label, Sub: hit.Sub}
		serviceID := model.ServiceKey{ClusterKey: hit.Cluster, Service: hit.Service}.ID()
		switch {
		case hit.Kind == "cluster", hit.Kind == "task" && hit.Service == "":
			item.Href = "/clusters/" + hit.Cluster.ID()
		case hit.Kind == "service":
			item.Href = "/services/" + serviceID + "/overview"
		default:
			item.Href = "/services/" + serviceID + "/tasks"
		}
		items = append(items, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
