package scan

// FixDiff compares the open findings of a running image with those of a
// candidate: fixes are open findings the candidate lacks, introduces are
// candidate findings that are not open today. Each key counts once.
func FixDiff(open, candidate []FindingKey) (fixes, introduces int) {
	openSet := make(map[FindingKey]struct{}, len(open))
	for _, key := range open {
		openSet[key] = struct{}{}
	}
	candidateSet := make(map[FindingKey]struct{}, len(candidate))
	for _, key := range candidate {
		candidateSet[key] = struct{}{}
	}
	for key := range openSet {
		if _, still := candidateSet[key]; !still {
			fixes++
		}
	}
	for key := range candidateSet {
		if _, had := openSet[key]; !had {
			introduces++
		}
	}
	return fixes, introduces
}
