package api

import (
	"time"

	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// InvestigationSubjectDTO says what was investigated. Fields that do not apply to the subject kind are empty strings.
type InvestigationSubjectDTO struct {
	Kind      string `json:"kind"`
	ServiceID string `json:"service_id"`
	Service   string `json:"service"`
	Cluster   string `json:"cluster"`
	ClusterID string `json:"cluster_id"`
	AccountID string `json:"account_id"`
	Region    string `json:"region"`
}

// InvestigationAlertDTO is the alert that started the investigation, as it was when the investigation was requested.
type InvestigationAlertDTO struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// InvestigationDTO is one investigation without its evidence. Summary is empty unless the state is done.
type InvestigationDTO struct {
	ID              int64                   `json:"id"`
	State           string                  `json:"state"`
	Reason          string                  `json:"reason"`
	Subject         InvestigationSubjectDTO `json:"subject"`
	Alert           InvestigationAlertDTO   `json:"alert"`
	RequestedAt     time.Time               `json:"requested_at"`
	StartedAt       *time.Time              `json:"started_at"`
	FinishedAt      *time.Time              `json:"finished_at"`
	Summary         string                  `json:"summary"`
	SummaryReplaced bool                    `json:"summary_replaced"`
}

// InvestigationMemberDTO is one service of a cluster or account investigation.
type InvestigationMemberDTO struct {
	ServiceID string `json:"service_id"`
	Service   string `json:"service"`
	Cluster   string `json:"cluster"`
}

// InvestigationEvidenceDTO is one evidence item the reader may see. Action is the permission it needed: view or logs.
type InvestigationEvidenceDTO struct {
	Ordinal   int    `json:"ordinal"`
	Tool      string `json:"tool"`
	Action    string `json:"action"`
	ServiceID string `json:"service_id"`
	Service   string `json:"service"`
	Note      string `json:"note"`
	Excerpt   string `json:"excerpt"`
}

// InvestigationDetailDTO adds the members and the evidence filtered for this reader. EvidenceHidden counts the items
// the reader may not see; nothing else about them is sent.
type InvestigationDetailDTO struct {
	InvestigationDTO
	Members        []InvestigationMemberDTO   `json:"members"`
	Evidence       []InvestigationEvidenceDTO `json:"evidence"`
	EvidenceHidden int                        `json:"evidence_hidden"`
}

// AlertInvestigationDTO is the link an alert carries to its open investigation.
type AlertInvestigationDTO struct {
	ID     int64  `json:"id"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

func investigationDTO(v store.Investigation) InvestigationDTO {
	subject := InvestigationSubjectDTO{Kind: v.SubjectKind, AccountID: v.AccountID, Region: v.Region}
	switch v.SubjectKind {
	case "service":
		key := investigationServiceKey(v)
		subject.ServiceID, subject.Service = key.ID(), v.ServiceName
		subject.Cluster, subject.ClusterID = v.ClusterName, key.ClusterKey.ID()
	case "cluster":
		subject.Cluster, subject.ClusterID = v.ClusterName, investigationClusterKey(v).ID()
	}
	out := InvestigationDTO{
		ID: v.ID, State: v.State, Reason: v.Reason, Subject: subject,
		Alert:       InvestigationAlertDTO{Kind: v.AlertKind, Title: v.AlertTitle},
		RequestedAt: v.RequestedAt, StartedAt: v.StartedAt, FinishedAt: v.FinishedAt,
	}
	if v.State == store.InvestigationDone {
		out.Summary, out.SummaryReplaced = v.Summary, v.SummaryReplaced
	}
	return out
}

func investigationClusterKey(v store.Investigation) model.ClusterKey {
	return model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: v.ClusterName}
}

func investigationServiceKey(v store.Investigation) model.ServiceKey {
	return model.ServiceKey{ClusterKey: investigationClusterKey(v), Service: v.ServiceName}
}

// investigationMemberKey is a member's service. A group investigation keeps the account and region of its subject.
func investigationMemberKey(v store.Investigation, m store.InvestigationMember) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: v.AccountID, Region: v.Region, Cluster: m.Cluster}, Service: m.Service}
}
