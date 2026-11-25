package investigate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// maxAlertLines is how many services the alert of a group lists before "and N more".
const maxAlertLines = 10

// groupFact is what a group's stored title says after the place: counts or fixed words, never a name.
var groupFact = regexp.MustCompile(`: ([0-9]{1,9} of [0-9]{1,9} services failing|unreachable)\z`)

// taskAlert is the alert as the agent is told it, scrubbed and cut to what the agent takes; it frames the text as data.
// The stored text of a group names services whether or not the identity may View them, and quotes AWS errors, so none
// of it is sent: the title and the text are written anew from the subject and the members visible now.
func taskAlert(v store.Investigation, visible []model.ServiceKey) agentclient.TaskAlert {
	alert := agentclient.TaskAlert{Kind: agentText(v.AlertKind), OpenedAt: v.AlertOpenedAt}
	switch v.SubjectKind {
	case "service":
		alert.Title, alert.Text = agentText(v.AlertTitle), agentText(v.AlertText)
	case "cluster", "account":
		alert.Title, alert.Text = agentText(groupTitle(v)), agentText(groupText(v.SubjectKind, visible))
	}
	return alert
}

// groupTitle names the place by its id, as a display name is free text, and keeps the counts of the stored title.
func groupTitle(v store.Investigation) string {
	place := v.ClusterName
	if v.SubjectKind == "account" {
		place = v.AccountID
		if v.Region != "" {
			place += " / " + v.Region
		}
	}
	fact := "services failing"
	if found := groupFact.FindStringSubmatch(v.AlertTitle); found != nil {
		fact = found[1]
	}
	return place + ": " + fact
}

func groupText(subjectKind string, visible []model.ServiceKey) string {
	if len(visible) == 0 {
		return ""
	}
	lines := []string{"Services that were failing when the alert was raised, of those you can see:"}
	for i, key := range visible {
		if i == maxAlertLines {
			lines = append(lines, fmt.Sprintf("and %d more", len(visible)-maxAlertLines))
			break
		}
		name := nameLine(key.Service)
		if subjectKind == "account" {
			name = nameLine(key.Cluster) + " / " + name
		}
		lines = append(lines, "- "+name)
	}
	return strings.Join(lines, "\n")
}

// nameLine puts a name on one line, so that it cannot pass for another service.
func nameLine(name string) string { return strings.Join(strings.Fields(name), " ") }
