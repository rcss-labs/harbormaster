package api

import (
	"crypto/rand"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

var roleARNPattern = regexp.MustCompile(`^arn:aws:iam::(\d{12}):role/.+$`)

const (
	roleARNMessage        = "Role ARN must look like arn:aws:iam::123456789012:role/name"
	accountNameMessage    = "Give the account a name."
	accountRegionsMessage = "Pick at least one region."
	externalIDMessage     = "External ID is required."
	accountConflictText   = "That AWS account is already connected."

	externalIDLength   = 24
	externalIDAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// AccountDTO is a connected AWS account on the admin accounts screen.
type AccountDTO struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	AccountID  string     `json:"account_id"`
	RoleARN    string     `json:"role_arn"`
	ExternalID string     `json:"external_id"`
	Regions    []string   `json:"regions"`
	Enabled    bool       `json:"enabled"`
	Clusters   int        `json:"clusters"`
	Status     string     `json:"status"`
	LastOKAt   *time.Time `json:"last_ok_at"`
	LastError  string     `json:"last_error"`
}

// RuntimeDTO describes the running server: its ECS home and the poll intervals in effect.
type RuntimeDTO struct {
	Cluster   string            `json:"cluster"`
	Service   string            `json:"service"`
	Intervals map[string]string `json:"intervals"`
}

// AccountsDTO is the body of GET /api/accounts.
type AccountsDTO struct {
	Items   []AccountDTO `json:"items"`
	Runtime RuntimeDTO   `json:"runtime"`
}

// RegionResultDTO is the outcome of probing one region for a role.
type RegionResultDTO struct {
	Region   string `json:"region"`
	OK       bool   `json:"ok"`
	Clusters int    `json:"clusters"`
	Error    string `json:"error"`
}

// accountBody is the request body for creating, testing and updating accounts.
type accountBody struct {
	Name       string   `json:"name"`
	RoleARN    string   `json:"role_arn"`
	ExternalID string   `json:"external_id"`
	Regions    []string `json:"regions"`
}

// clusterCountsByAccount counts the cluster snapshots of every account, by AWS account ID.
func (s *Server) clusterCountsByAccount(r *http.Request) (map[string]int, error) {
	rows, err := s.Store.ListClusters(r.Context(), store.TargetFilter{}, adminVisibility)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, row := range rows {
		counts[row.Key.AccountID]++
	}
	return counts, nil
}

// accountDTO maps a stored account to its DTO. Status is disabled when the account is off,
// error when the last poll failed, and connected otherwise.
func accountDTO(a store.AWSAccount, clusters int) AccountDTO {
	status := "connected"
	switch {
	case !a.Enabled:
		status = "disabled"
	case a.LastError != "":
		status = "error"
	}
	regions := a.Regions
	if regions == nil {
		regions = []string{}
	}
	return AccountDTO{ID: a.ID, Name: a.Name, AccountID: a.AccountID, RoleARN: a.RoleARN, ExternalID: a.ExternalID,
		Regions: regions, Enabled: a.Enabled, Clusters: clusters, Status: status, LastOKAt: a.LastOKAt, LastError: a.LastError}
}

func (s *Server) runtimeDTO() RuntimeDTO {
	poll := s.Cfg.Poll
	return RuntimeDTO{
		Cluster: "harbormaster",
		Service: "harbormaster-server",
		Intervals: map[string]string{
			"services":       poll.Services.String(),
			"running_tasks":  poll.RunningTasks.String(),
			"full_tasks":     poll.FullTasks.String(),
			"stopped_tasks":  poll.StoppedTasks.String(),
			"clusters":       poll.Clusters.String(),
			"list_services":  poll.ServiceList.String(),
			"counts_history": poll.CountsHistory.String(),
		},
	}
}

func (s *Server) listAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.Store.ListAccounts(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	counts, err := s.clusterCountsByAccount(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]AccountDTO, 0, len(accounts))
	for _, a := range accounts {
		items = append(items, accountDTO(a, counts[a.AccountID]))
	}
	writeJSON(w, http.StatusOK, AccountsDTO{Items: items, Runtime: s.runtimeDTO()})
}

// newExternalID returns a random 24-character alphanumeric external ID. Bytes at or above 248
// (the largest multiple of 62 below 256) are rejected so every character is equally likely.
func newExternalID() (string, error) {
	out := make([]byte, 0, externalIDLength)
	buf := make([]byte, 1)
	for len(out) < externalIDLength {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= 248 {
			continue
		}
		out = append(out, externalIDAlphabet[int(buf[0])%len(externalIDAlphabet)])
	}
	return string(out), nil
}

func (s *Server) getExternalID(w http.ResponseWriter, r *http.Request) {
	id, err := newExternalID()
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"external_id": id})
}

// validateRegions rejects an empty region list.
func validateRegions(regions []string) error {
	if len(regions) == 0 {
		return invalid(accountRegionsMessage)
	}
	return nil
}

// validateAccountName returns the trimmed name, or a 422 when it is empty.
func validateAccountName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", invalid(accountNameMessage)
	}
	return trimmed, nil
}

// testAccounts probes a role and its regions without saving anything.
func (s *Server) testAccounts(w http.ResponseWriter, r *http.Request) {
	var body accountBody
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	if err := validateRegions(body.Regions); err != nil {
		s.fail(w, err)
		return
	}
	results := s.Prober.Probe(r.Context(), body.RoleARN, body.ExternalID, body.Regions)
	out := make([]RegionResultDTO, 0, len(results))
	for _, res := range results {
		out = append(out, RegionResultDTO{Region: res.Region, OK: res.OK, Clusters: res.Clusters, Error: res.Error})
	}
	writeJSON(w, http.StatusOK, map[string]any{"regions": out})
}

func (s *Server) createAccount(w http.ResponseWriter, r *http.Request) {
	var body accountBody
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	match := roleARNPattern.FindStringSubmatch(body.RoleARN)
	if match == nil {
		s.fail(w, invalid(roleARNMessage))
		return
	}
	accountID := match[1]
	name, err := validateAccountName(body.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := validateRegions(body.Regions); err != nil {
		s.fail(w, err)
		return
	}
	if body.ExternalID == "" {
		s.fail(w, invalid(externalIDMessage))
		return
	}
	created, err := s.Store.CreateAccount(r.Context(), store.AWSAccount{
		Name: name, AccountID: accountID, RoleARN: body.RoleARN, ExternalID: body.ExternalID,
		Regions: body.Regions, Enabled: true,
	})
	if errors.Is(err, store.ErrConflict) {
		s.fail(w, &APIError{Status: http.StatusConflict, Code: "conflict", Message: accountConflictText})
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.auditAdmin(r, "account_connect", "account:"+name,
		"Connected AWS account "+name+" ("+accountID+").", nil)
	writeJSON(w, http.StatusCreated, accountDTO(created, 0))
}

// invalidate drops cached AWS credentials and clients for an account when the implementation
// supports it. The real client does; the fake does not need to.
func (s *Server) invalidate(accountID string) {
	if inv, ok := s.Clients.(interface{ Invalidate(accountID string) }); ok {
		inv.Invalidate(accountID)
	}
}

func (s *Server) updateAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	var body accountBody
	if err := decodeJSON(w, r, &body); err != nil {
		s.fail(w, err)
		return
	}
	name, err := validateAccountName(body.Name)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := validateRegions(body.Regions); err != nil {
		s.fail(w, err)
		return
	}
	before, err := s.Store.GetAccount(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.UpdateAccount(r.Context(), id, name, body.Regions); err != nil {
		s.fail(w, err)
		return
	}
	s.invalidate(before.AccountID)
	s.auditAdmin(r, "account_update", "account:"+name, "Updated AWS account "+name+".", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		s.fail(w, store.ErrNotFound)
		return
	}
	account, err := s.Store.GetAccount(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := s.Store.DeleteAccount(r.Context(), id); err != nil {
		s.fail(w, err)
		return
	}
	s.invalidate(account.AccountID)
	s.auditAdmin(r, "account_disconnect", "account:"+account.Name, "Disconnected AWS account "+account.Name+".", nil)
	w.WriteHeader(http.StatusNoContent)
}
