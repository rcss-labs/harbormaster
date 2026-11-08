package fake

import "github.com/rcss-labs/harbormaster/internal/scan"

// fakeFinding is one table entry. The finding disappears from a tag that is
// at least fixedAtStep steps newer than the seeded running tag.
type fakeFinding struct {
	finding     scan.Finding
	cvss        float64
	fixedAtStep int
}

const (
	fakeCleanPackageCount    = 42
	fakeFindingsPackageCount = 187
)

func debFinding(id, severity string, cvss float64, name, version, fixed string) scan.Finding {
	f := scan.Finding{
		VulnID: id, Severity: severity, PackageName: name, PackageVersion: version,
		PackageType: "deb", Location: "/var/lib/dpkg/status", FixedVersion: fixed,
		FixState: "fixed", DataSource: "fake-advisory-feed", URL: "https://example.com/advisory/" + id,
	}
	if fixed == "" {
		f.FixState = "not-fixed"
	}
	return f
}

func withType(f scan.Finding, packageType, location string) scan.Finding {
	f.PackageType, f.Location = packageType, location
	return f
}

// fakeFindingTable holds the findings of the seeded running image of each
// repository. Any other repository has none. Ids are CVE-0000-NNNN only.
var fakeFindingTable = map[string][]fakeFinding{
	// edge-nginx 1.27.3 is clean: one rebuild fixes all three.
	"edge-nginx": {
		{finding: debFinding("CVE-0000-1042", scan.SeverityCritical, 9.8, "openssl", "3.0.13", "3.0.15"), cvss: 9.8, fixedAtStep: 1},
		{finding: debFinding("CVE-0000-2210", scan.SeverityHigh, 7.5, "nginx", "1.27.2", "1.27.3"), cvss: 7.5, fixedAtStep: 1},
		{finding: debFinding("CVE-0000-3188", scan.SeverityMedium, 5.3, "zlib", "1.3", "1.3.1"), cvss: 5.3, fixedAtStep: 1},
	},
	"payments-api": {
		{finding: withType(debFinding("CVE-0000-1877", scan.SeverityCritical, 9.1, "golang.org/x/net", "0.28.0", "0.33.0"), "go-module", "/app/payments-api"), cvss: 9.1, fixedAtStep: 1},
		{finding: debFinding("CVE-0000-4410", scan.SeverityHigh, 7.8, "glibc", "2.36", "2.36-9"), cvss: 7.8, fixedAtStep: 2},
	},
	"jenkins": {
		{finding: withType(debFinding("CVE-0000-5120", scan.SeverityHigh, 8.1, "jenkins-core", "2.479.1", "2.479.2"), "java-archive", "/usr/share/jenkins/jenkins.war"), cvss: 8.1, fixedAtStep: 1},
		{finding: withType(debFinding("CVE-0000-5121", scan.SeverityMedium, 5.4, "git-plugin", "5.5.2", "5.6.0"), "java-archive", "/var/jenkins_home/plugins/git.jpi"), cvss: 5.4, fixedAtStep: 2},
		{finding: withType(debFinding("CVE-0000-5122", scan.SeverityMedium, 5.0, "commons-text", "1.9", ""), "java-archive", "/usr/share/jenkins/jenkins.war"), cvss: 5.0, fixedAtStep: 3},
	},
	"catalog-api": {
		{finding: debFinding("CVE-0000-6023", scan.SeverityMedium, 5.9, "libxml2", "2.9.14", ""), cvss: 5.9, fixedAtStep: 1},
	},
}

// FindingsFor is what the fake scanner reports for repository:tag. A tag newer
// than the seeded running tag drops entries from the front, one per step.
func (s *Scanner) FindingsFor(repository, tag string) []scan.Finding {
	steps := s.stepsAhead(repository, tag)
	var findings []scan.Finding
	for _, entry := range fakeFindingTable[repository] {
		if steps >= entry.fixedAtStep {
			continue
		}
		finding := entry.finding
		score := entry.cvss
		finding.CVSS = &score
		findings = append(findings, finding)
	}
	return findings
}

// stepsAhead is how many tags newer than the seeded running tag this tag is.
func (s *Scanner) stepsAhead(repository, tag string) int {
	e := s.Estate
	e.Mu.Lock()
	defer e.Mu.Unlock()
	position := func(want string) int {
		for i, t := range e.Repositories[repository] {
			if t == want {
				return i
			}
		}
		return -1
	}
	running, candidate := position(e.RunningTags[repository]), position(tag)
	if running < 0 || candidate < 0 || candidate >= running {
		return 0
	}
	return running - candidate
}
