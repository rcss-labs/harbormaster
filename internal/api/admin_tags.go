package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

var tagNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 _-]{0,23}$`)

const (
	tagNameMessage    = "Use 1–24 letters, digits, spaces, hyphens or underscores, starting with a letter."
	tagColorMessage   = "Pick one of the tag colours."
	devDefaultMessage = "Dev is the default tag."
)

var tagColors = map[string]bool{"neutral": true, "red": true, "amber": true, "blue": true, "violet": true, "green": true}

// TagUserDTO is a non-admin user holding some permission on a tag.
type TagUserDTO struct {
	Username string `json:"username"`
	Preset   string `json:"preset"`
}

// TagItemDTO is a row of the tag management screen.
type TagItemDTO struct {
	ID                int64        `json:"id"`
	Name              string       `json:"name"`
	Color             string       `json:"color"`
	Builtin           bool         `json:"builtin"`
	Clusters          int          `json:"clusters"`
	ServicesDirect    int          `json:"services_direct"`
	ServicesEffective int          `json:"services_effective"`
	Users             []TagUserDTO `json:"users"`
}

// buildTagItems returns every tag as a list item: Dev first, then by name.
func (s *Server) buildTagItems(r *http.Request) ([]TagItemDTO, error) {
	ctx := r.Context()
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.Store.TagCounts(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	perms, err := s.Store.AllPermissions(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(tags, func(i, j int) bool {
		if tags[i].Builtin != tags[j].Builtin {
			return tags[i].Builtin
		}
		return tags[i].Name < tags[j].Name
	})
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	items := make([]TagItemDTO, 0, len(tags))
	for _, t := range tags {
		c := counts[t.ID]
		item := TagItemDTO{ID: int64(t.ID), Name: t.Name, Color: t.Color, Builtin: t.Builtin,
			Clusters: c.Clusters, ServicesDirect: c.ServicesDirect, ServicesEffective: c.ServicesEffective, Users: []TagUserDTO{}}
		for _, u := range users {
			if set, ok := perms[u.ID][t.ID]; ok && !u.Admin {
				item.Users = append(item.Users, TagUserDTO{Username: u.Username, Preset: authz.PresetName(set)})
			}
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Server) tagItem(r *http.Request, id int64) (TagItemDTO, error) {
	items, err := s.buildTagItems(r)
	if err != nil {
		return TagItemDTO{}, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return TagItemDTO{}, store.ErrNotFound
}

func (s *Server) listTagItems(w http.ResponseWriter, r *http.Request) {
	items, err := s.buildTagItems(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ItemsResponse[TagItemDTO]{Items: items})
}

// decodeTagBody reads and validates {name, color}; the name is returned trimmed.
func decodeTagBody(w http.ResponseWriter, r *http.Request) (name, color string, err error) {
	var body struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return "", "", err
	}
	name = strings.TrimSpace(body.Name)
	if !tagNamePattern.MatchString(name) {
		return "", "", invalid(tagNameMessage)
	}
	if !tagColors[body.Color] {
		return "", "", invalid(tagColorMessage)
	}
	return name, body.Color, nil
}

func tagConflict(name string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "conflict", Message: "A tag called " + name + " already exists."}
}

func (s *Server) createTag(w http.ResponseWriter, r *http.Request) {
	name, color, err := decodeTagBody(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	tag, err := s.Store.CreateTag(r.Context(), name, color)
	if errors.Is(err, store.ErrConflict) {
		s.fail(w, tagConflict(name))
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "tag_create", "tag:"+tag.Name, "Created tag "+tag.Name+".", map[string]any{"color": tag.Color})
	item, err := s.tagItem(r, int64(tag.ID))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *Server) updateTag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	name, color, err := decodeTagBody(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	before, err := s.Store.GetTag(r.Context(), authz.TagID(id))
	if err != nil {
		s.fail(w, err)
		return
	}
	switch err := s.Store.UpdateTag(r.Context(), before.ID, name, color); {
	case errors.Is(err, store.ErrBuiltin):
		s.fail(w, invalid(devDefaultMessage))
		return
	case errors.Is(err, store.ErrConflict):
		s.fail(w, tagConflict(name))
		return
	case err != nil:
		s.fail(w, err)
		return
	}
	switch {
	case before.Name != name:
		s.auditAdmin(r, "tag_update", "tag:"+name, "Renamed tag "+before.Name+" to "+name+".",
			map[string]any{"old_name": before.Name, "color": color})
	case before.Color != color:
		s.auditAdmin(r, "tag_update", "tag:"+name, "Changed the colour of tag "+name+".",
			map[string]any{"old_color": before.Color, "color": color})
	}
	item, err := s.tagItem(r, id)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) deleteTag(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	tag, err := s.Store.GetTag(r.Context(), authz.TagID(id))
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteTag(r.Context(), tag.ID); err != nil {
		if errors.Is(err, store.ErrBuiltin) {
			s.fail(w, invalid(devDefaultMessage))
			return
		}
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "tag_delete", "tag:"+tag.Name, "Deleted tag "+tag.Name+".", nil)
	w.WriteHeader(http.StatusNoContent)
}

type idNameDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) tagImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	if _, err := s.Store.GetTag(r.Context(), authz.TagID(id)); err != nil {
		s.fail(w, err)
		return
	}
	impact, err := s.Store.TagImpact(r.Context(), authz.TagID(id))
	if err != nil {
		s.fail(w, err)
		return
	}
	out := TagImpactResponse{Clusters: []idNameDTO{}, Services: []idNameDTO{}, Users: impact.Usernames}
	for _, k := range impact.Clusters {
		out.Clusters = append(out.Clusters, idNameDTO{ID: k.ID(), Name: k.Cluster})
	}
	for _, k := range impact.Services {
		out.Services = append(out.Services, idNameDTO{ID: k.ID(), Name: k.Service})
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- assignments ----

type serviceAssignmentDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	TagID     *int64 `json:"tag_id"`
	Effective TagDTO `json:"effective"`
}

type clusterAssignmentDTO struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name"`
	AccountName string                 `json:"account_name"`
	Region      string                 `json:"region"`
	TagID       *int64                 `json:"tag_id"`
	Effective   TagDTO                 `json:"effective"`
	Services    []serviceAssignmentDTO `json:"services"`
}

var adminVisibility = store.Visibility{Admin: true}

func (s *Server) listTagAssignments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	filter := store.TargetFilter{AccountID: r.URL.Query().Get("account"), Region: r.URL.Query().Get("region")}
	clusters, err := s.Store.ListClusters(ctx, filter, adminVisibility)
	if err != nil {
		s.fail(w, err)
		return
	}
	services, err := s.Store.ListServices(ctx, filter, "", adminVisibility)
	if err != nil {
		s.fail(w, err)
		return
	}
	names, err := s.accountNames(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	byCluster := map[model.ClusterKey][]serviceAssignmentDTO{}
	for _, svc := range services {
		dto := serviceAssignmentDTO{ID: svc.Key.ID(), Name: svc.Key.Service, Effective: tags[svc.EffectiveTagID]}
		dto.Effective.Inherited = svc.TagInherited
		if !svc.TagInherited {
			id := int64(svc.EffectiveTagID)
			dto.TagID = &id
		}
		byCluster[svc.Key.ClusterKey] = append(byCluster[svc.Key.ClusterKey], dto)
	}
	out := TagAssignmentsResponse{Clusters: []clusterAssignmentDTO{}}
	for _, c := range clusters {
		dto := clusterAssignmentDTO{ID: c.Key.ID(), Name: c.Key.Cluster, AccountName: names[c.Key.AccountID],
			Region: c.Key.Region, Effective: tags[c.TagID], Services: byCluster[c.Key]}
		if dto.Services == nil {
			dto.Services = []serviceAssignmentDTO{}
		}
		dto.Effective.Inherited = !c.TagSet
		if c.TagSet {
			id := int64(c.TagID)
			dto.TagID = &id
		}
		out.Clusters = append(out.Clusters, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// decodeAssignedTag reads {"tag_id": number|null}. A missing key is a 422 so a malformed
// request is never mistaken for "untag"; null returns nil.
func decodeAssignedTag(w http.ResponseWriter, r *http.Request) (*authz.TagID, error) {
	var body struct {
		TagID json.RawMessage `json:"tag_id"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return nil, err
	}
	if len(body.TagID) == 0 {
		return nil, invalid("tag_id is required (a tag id, or null).")
	}
	if string(body.TagID) == "null" {
		return nil, nil
	}
	var id int64
	if err := json.Unmarshal(body.TagID, &id); err != nil {
		return nil, invalid("tag_id must be a tag id or null.")
	}
	tag := authz.TagID(id)
	return &tag, nil
}

var errTagNotFound = &APIError{Status: http.StatusNotFound, Code: "not_found", Message: "Tag not found."}

// tagNameOf resolves a tag name; ids that no longer exist read as "unknown".
func tagNameOf(tags map[authz.TagID]TagDTO, id authz.TagID) string {
	if t, ok := tags[id]; ok {
		return t.Name
	}
	return "unknown"
}

func (s *Server) assignClusterTag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	newTag, err := decodeAssignedTag(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	key, err := model.ParseClusterID(r.PathValue("clusterId"))
	if err != nil {
		s.fail(w, errNotFound)
		return
	}
	cluster, err := s.Store.GetCluster(ctx, key, adminVisibility)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if newTag != nil {
		if _, ok := tags[*newTag]; !ok {
			s.fail(w, errTagNotFound)
			return
		}
	}
	dev, err := s.Store.DevTag(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	oldTag, wasSet := cluster.TagID, cluster.TagSet
	if (newTag == nil && !wasSet) || (newTag != nil && wasSet && *newTag == oldTag) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	newEffective := dev.ID
	if newTag != nil {
		newEffective = *newTag
		err = s.Store.SetClusterTag(ctx, key, *newTag)
	} else {
		err = s.Store.ClearClusterTag(ctx, key)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	oldName, newName := tagNameOf(tags, oldTag), tagNameOf(tags, newEffective)
	s.auditAdmin(r, "tag_assign", "cluster:"+key.Cluster,
		"Tagged cluster "+key.Cluster+" as "+newName+" (was "+oldName+").",
		map[string]any{"cluster_id": key.ID(), "from": oldName, "to": newName})

	services, err := s.Store.ListServices(ctx, store.TargetFilter{AccountID: key.AccountID, Region: key.Region}, key.Cluster, adminVisibility)
	if err != nil {
		s.log().Error("tag assignment: list services for oplog failed", "cluster", key.Cluster, "error", err)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	actor, _ := auth.UserFrom(ctx)
	text := "Changed tag of cluster " + key.Cluster + " from " + oldName + " to " + newName + "."
	for _, svc := range services {
		name := svc.Key.Service
		if err := s.Store.AppendOperation(ctx, store.Operation{
			AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &name,
			ActorUserID: &actor.ID, Source: "harbormaster", Text: text,
		}); err != nil {
			s.log().Error("tag assignment: oplog write failed", "service", name, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) assignServiceTag(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	newTag, err := decodeAssignedTag(w, r)
	if err != nil {
		s.fail(w, err)
		return
	}
	key, err := serviceKeyFrom(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	svc, err := s.Store.GetServiceView(ctx, key, adminVisibility)
	if err != nil {
		s.fail(w, err)
		return
	}
	tags, err := s.tagDTOs(ctx)
	if err != nil {
		s.fail(w, err)
		return
	}
	if newTag != nil {
		if _, ok := tags[*newTag]; !ok {
			s.fail(w, errTagNotFound)
			return
		}
	}
	// The tag this service inherits when it has none of its own.
	clusterEffective, err := s.clusterEffectiveTag(r, key.ClusterKey)
	if err != nil {
		s.fail(w, err)
		return
	}
	inheritText := "inherit (" + tagNameOf(tags, clusterEffective) + ")"
	oldText, hadDirect := inheritText, !svc.TagInherited
	if hadDirect {
		oldText = tagNameOf(tags, svc.EffectiveTagID)
	}
	if (newTag == nil && !hadDirect) || (newTag != nil && hadDirect && *newTag == svc.EffectiveTagID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	newText := inheritText
	if newTag != nil {
		newText = tagNameOf(tags, *newTag)
	}
	if err := s.Store.SetServiceTag(ctx, key, newTag); err != nil {
		s.fail(w, err)
		return
	}
	text := "Tagged service " + key.Service + " as " + newText + " (was " + oldText + ")."
	s.auditAdmin(r, "tag_assign", "service:"+key.Cluster+"/"+key.Service, text,
		map[string]any{"service_id": key.ID(), "from": oldText, "to": newText})
	actor, _ := auth.UserFrom(ctx)
	name := key.Service
	if err := s.Store.AppendOperation(ctx, store.Operation{
		AccountID: key.AccountID, Region: key.Region, Cluster: key.Cluster, Service: &name,
		ActorUserID: &actor.ID, Source: "harbormaster", Text: text,
	}); err != nil {
		s.log().Error("tag assignment: oplog write failed", "service", name, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// clusterEffectiveTag is the cluster's own tag, else Dev.
func (s *Server) clusterEffectiveTag(r *http.Request, key model.ClusterKey) (authz.TagID, error) {
	id, set, err := s.Store.ClusterTag(r.Context(), key)
	if err != nil {
		return 0, err
	}
	if set {
		return id, nil
	}
	dev, err := s.Store.DevTag(r.Context())
	return dev.ID, err
}
