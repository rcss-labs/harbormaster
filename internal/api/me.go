package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/authz"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// allActions is every permission bit; admins hold it on every tag.
const allActions = int(authz.View | authz.Logs | authz.Operate | authz.Deploy | authz.Edit | authz.Terminal)

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, _ := auth.UserFrom(r.Context())
	sess, _ := auth.SessionFrom(r.Context())
	dto, err := s.buildMe(r.Context(), user, sess.CSRFToken, auth.SubjectFrom(r.Context()))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// buildMe assembles the MeDTO. Accounts: admins see every enabled account; others only accounts
// with at least one visible cluster, narrowed to the regions where they can see one.
func (s *Server) buildMe(ctx context.Context, user store.User, csrf string, subject authz.Subject) (MeDTO, error) {
	tags, err := s.Store.ListTags(ctx)
	if err != nil {
		return MeDTO{}, err
	}
	accounts, err := s.Store.ListAccounts(ctx)
	if err != nil {
		return MeDTO{}, err
	}
	clusters, err := s.Store.ListClusters(ctx, store.TargetFilter{}, visibilityOf(subject))
	if err != nil {
		return MeDTO{}, err
	}
	visibleRegions := map[string]map[string]bool{} // account_id -> region -> has a visible cluster
	for _, c := range clusters {
		if visibleRegions[c.Key.AccountID] == nil {
			visibleRegions[c.Key.AccountID] = map[string]bool{}
		}
		visibleRegions[c.Key.AccountID][c.Key.Region] = true
	}

	dto := MeDTO{
		ID: user.ID, Username: user.Username, Admin: subject.Admin,
		MustChangePassword: user.MustChangePassword, CSRFToken: csrf,
		Perms: map[string]int{}, Tags: make([]TagDTO, 0, len(tags)),
		Accounts: []MeAccountDTO{}, Regions: []string{},
	}
	for _, t := range tags {
		dto.Tags = append(dto.Tags, TagDTO{ID: int64(t.ID), Name: t.Name, Color: t.Color, Builtin: t.Builtin})
		if subject.Admin {
			dto.Perms[strconv.FormatInt(int64(t.ID), 10)] = allActions
		}
	}
	if !subject.Admin {
		for id, set := range subject.Perms {
			dto.Perms[strconv.FormatInt(int64(id), 10)] = int(set)
		}
	}

	union := map[string]bool{}
	for _, a := range accounts {
		if !a.Enabled {
			continue
		}
		regions := append([]string{}, a.Regions...)
		if !subject.Admin {
			regions = regions[:0]
			for region := range visibleRegions[a.AccountID] {
				regions = append(regions, region)
			}
			if len(regions) == 0 {
				continue
			}
		}
		sort.Strings(regions)
		for _, region := range regions {
			union[region] = true
		}
		dto.Accounts = append(dto.Accounts, MeAccountDTO{ID: a.ID, Name: a.Name, AccountID: a.AccountID, Regions: regions})
	}
	for region := range union {
		dto.Regions = append(dto.Regions, region)
	}
	sort.Strings(dto.Regions)
	return dto, nil
}
