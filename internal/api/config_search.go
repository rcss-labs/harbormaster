package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/rcss-labs/harbormaster/internal/configsearch"
	"github.com/rcss-labs/harbormaster/internal/scan"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// configSearchCap is the most rows GET /api/config-search returns; a var so a test can lower it.
var configSearchCap = 2000

// ConfigMatchDTO names one value that satisfied a term: the field, the container ("" for roles) and the value.
type ConfigMatchDTO struct {
	Field     string `json:"field"`
	Container string `json:"container"`
	Value     string `json:"value"`
}

// ConfigContainerDTO is one container of the matched revision. Only names are ever present, never values.
type ConfigContainerDTO struct {
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	Tag         string   `json:"tag"`
	BaseImage   string   `json:"base_image"`
	LogGroup    string   `json:"log_group"`
	Ports       []int    `json:"ports"`
	EnvNames    []string `json:"env_names"`
	SecretNames []string `json:"secret_names"`
}

// ConfigSearchItemDTO is one matching service.
type ConfigSearchItemDTO struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Cluster      ClusterRef `json:"cluster"`
	AccountID    string     `json:"account_id"`
	AccountName  string     `json:"account_name"`
	Region       string     `json:"region"`
	Tag          TagDTO     `json:"tag"`
	TagInherited bool       `json:"tag_inherited"`
	Family       string     `json:"family"`
	// Revision is the PRIMARY revision; MatchedRevision is the one that matched, and Previous is true when it is
	// an older revision that still has tasks.
	Revision        int                  `json:"revision"`
	MatchedRevision int                  `json:"matched_revision"`
	Previous        bool                 `json:"previous"`
	TaskRole        string               `json:"task_role"`
	ExecutionRole   string               `json:"execution_role"`
	Containers      []ConfigContainerDTO `json:"containers"`
	Matches         []ConfigMatchDTO     `json:"matches"`
}

// ConfigSearchResponse is the body of GET /api/config-search.
type ConfigSearchResponse struct {
	// Query is the normalized query text.
	Query     string                `json:"query"`
	Items     []ConfigSearchItemDTO `json:"items"`
	Total     int                   `json:"total"`
	Truncated bool                  `json:"truncated"`
	// Searched is the number of visible services in scope; Unindexed those whose current task definition is not
	// cached yet, which cannot match anything.
	Searched  int       `json:"searched"`
	Unindexed int       `json:"unindexed"`
	PolledAt  time.Time `json:"polled_at"`
}

// configMatch is one service that satisfied a query: the matched revision and what matched. It is the reusable
// selector for features that act on a config search result (Bulk changes).
type configMatch struct {
	Row        store.ConfigRow
	Revision   store.ConfigRevision
	Hits       []configsearch.Hit
	Containers []ConfigContainerDTO
}

// configSearchMatches evaluates q for the services visible in sc. Visibility and the shell filters are applied by
// the store query, so a service the caller cannot see is in neither the matches nor the counts.
func (s *Server) configSearchMatches(ctx context.Context, sc scope, q configsearch.Query) (matches []configMatch, searched, unindexed int, err error) {
	rows, err := s.Store.ConfigSearchRows(ctx, sc.Filter, sc.Vis)
	if err != nil {
		return nil, 0, 0, err
	}
	searched = len(rows)

	bases, err := s.configBaseImages(ctx, rows)
	if err != nil {
		return nil, 0, 0, err
	}
	for _, row := range rows {
		if len(row.Revisions) == 0 || !row.Revisions[0].Loaded {
			unindexed++
			continue
		}
		for _, rev := range row.Revisions {
			if !rev.Loaded {
				continue
			}
			containers := configContainerDTOs(rev, bases)
			hits, ok := configsearch.Match(q, configDoc(rev, containers))
			if !ok {
				continue
			}
			matches = append(matches, configMatch{Row: row, Revision: rev, Hits: hits, Containers: containers})
			break
		}
	}
	return matches, searched, unindexed, nil
}

// configBaseImages looks up the configured base image of every ECR repository the rows use, in one query.
func (s *Server) configBaseImages(ctx context.Context, rows []store.ConfigRow) (map[store.RepoKey]string, error) {
	seen := map[store.RepoKey]bool{}
	var keys []store.RepoKey
	for _, row := range rows {
		for _, rev := range row.Revisions {
			for _, c := range rev.Containers {
				if key, ok := ecrRepoKey(c.Image); ok && !seen[key] {
					seen[key] = true
					keys = append(keys, key)
				}
			}
		}
	}
	if len(keys) == 0 {
		return map[store.RepoKey]string{}, nil
	}
	return s.Store.RepoBases(ctx, keys)
}

func ecrRepoKey(image string) (store.RepoKey, bool) {
	ref := scan.ParseImageRef(image)
	if ref.Kind != scan.KindECR {
		return store.RepoKey{}, false
	}
	return store.RepoKey{AccountID: ref.AccountID, Region: ref.Region, Repository: ref.Repository}, true
}

func configContainerDTOs(rev store.ConfigRevision, bases map[store.RepoKey]string) []ConfigContainerDTO {
	out := make([]ConfigContainerDTO, 0, len(rev.Containers))
	for _, c := range rev.Containers {
		dto := ConfigContainerDTO{
			Name: c.Name, Image: c.Image, Tag: scan.ParseImageRef(c.Image).Tag, LogGroup: c.LogGroup,
			Ports: c.Ports, EnvNames: c.EnvNames, SecretNames: c.SecretNames,
		}
		if key, ok := ecrRepoKey(c.Image); ok {
			dto.BaseImage = bases[key]
		}
		out = append(out, dto)
	}
	return out
}

func configDoc(rev store.ConfigRevision, containers []ConfigContainerDTO) configsearch.Doc {
	doc := configsearch.Doc{TaskRoleARN: rev.TaskRoleARN, ExecutionRoleARN: rev.ExecutionRoleARN}
	for _, c := range containers {
		doc.Containers = append(doc.Containers, configsearch.Container{
			Name: c.Name, Image: c.Image, Tag: c.Tag, BaseImage: c.BaseImage, LogGroup: c.LogGroup,
			Ports: c.Ports, EnvNames: c.EnvNames, SecretNames: c.SecretNames,
		})
	}
	return doc
}

// getConfigSearch answers GET /api/config-search?q=&account=&region=&tag=. It is a read of names the caller can
// already read per service (the task definition tab), so nothing is audited or logged to the operations log.
func (s *Server) getConfigSearch(w http.ResponseWriter, r *http.Request) {
	sc, err := s.scopeFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	ctx := r.Context()
	q, err := configsearch.Parse(r.URL.Query().Get("q"))
	if err != nil {
		var parseErr *configsearch.ParseError
		if errors.As(err, &parseErr) {
			err = invalid(parseErr.Message)
		}
		s.fail(w, err)
		return
	}
	polledAt, err := s.Store.PolledAt(ctx, sc.Filter)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp := ConfigSearchResponse{Query: q.String(), Items: []ConfigSearchItemDTO{}, PolledAt: polledAt}
	if q.Empty() {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	matches, searched, unindexed, err := s.configSearchMatches(ctx, sc, q)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}

	items := make([]ConfigSearchItemDTO, 0, len(matches))
	for _, m := range matches {
		key := m.Row.Key
		item := ConfigSearchItemDTO{
			ID: key.ID(), Name: key.Service,
			Cluster:   ClusterRef{ID: key.ClusterKey.ID(), Name: key.Cluster},
			AccountID: key.AccountID, AccountName: names[key.AccountID], Region: key.Region,
			Tag: inheritedTag(tags, m.Row.EffectiveTagID, false), TagInherited: m.Row.TagInherited,
			Family: m.Row.Family, Revision: m.Row.Revisions[0].Revision,
			MatchedRevision: m.Revision.Revision, Previous: !m.Revision.Primary,
			TaskRole: m.Revision.TaskRoleARN, ExecutionRole: m.Revision.ExecutionRoleARN,
			Containers: m.Containers, Matches: make([]ConfigMatchDTO, 0, len(m.Hits)),
		}
		for _, h := range m.Hits {
			item.Matches = append(item.Matches, ConfigMatchDTO{Field: string(h.Field), Container: h.Container, Value: h.Value})
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].ID < items[j].ID
	})
	resp.Total, resp.Searched, resp.Unindexed = len(items), searched, unindexed
	if len(items) > configSearchCap {
		items = items[:configSearchCap]
	}
	resp.Items, resp.Truncated = items, resp.Total > len(items)
	writeJSON(w, http.StatusOK, resp)
}
