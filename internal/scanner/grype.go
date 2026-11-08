package scanner

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/scan"
)

type grypeReport struct {
	Matches []struct {
		Vulnerability struct {
			ID         string   `json:"id"`
			Severity   string   `json:"severity"`
			DataSource string   `json:"dataSource"`
			URLs       []string `json:"urls"`
			Fix        struct {
				Versions []string `json:"versions"`
				State    string   `json:"state"`
			} `json:"fix"`
			CVSS []struct {
				Metrics struct {
					BaseScore float64 `json:"baseScore"`
				} `json:"metrics"`
			} `json:"cvss"`
		} `json:"vulnerability"`
		Artifact struct {
			Name      string `json:"name"`
			Version   string `json:"version"`
			Type      string `json:"type"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
		} `json:"artifact"`
	} `json:"matches"`
}

// ParseGrypeOutput maps Grype's JSON report to findings. The leader validates
// them again; this only reshapes.
func ParseGrypeOutput(data []byte) ([]scan.Finding, error) {
	var report grypeReport
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, errors.New("grype output is not valid JSON")
	}
	findings := make([]scan.Finding, 0, len(report.Matches))
	for _, m := range report.Matches {
		v, a := m.Vulnerability, m.Artifact
		finding := scan.Finding{
			VulnID: v.ID, Severity: strings.ToLower(v.Severity), PackageName: a.Name,
			PackageVersion: a.Version, PackageType: a.Type, FixState: v.Fix.State, DataSource: v.DataSource,
		}
		if len(v.Fix.Versions) > 0 {
			finding.FixedVersion = v.Fix.Versions[0]
		}
		if len(v.CVSS) > 0 {
			score := v.CVSS[0].Metrics.BaseScore
			finding.CVSS = &score
		}
		if len(v.URLs) > 0 {
			finding.URL = v.URLs[0]
		}
		if len(a.Locations) > 0 {
			finding.Location = a.Locations[0].Path
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

var severityRank = map[string]int{
	scan.SeverityCritical: 0, scan.SeverityHigh: 1, scan.SeverityMedium: 2,
	scan.SeverityLow: 3, scan.SeverityNegligible: 4,
}

func rankOf(severity string) int {
	if rank, ok := severityRank[severity]; ok {
		return rank
	}
	return len(severityRank) // unknown sorts last and is dropped first
}
