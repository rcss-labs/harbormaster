package scan

// FailureCopy is FailureAction for an image in the given status. A timeout is only "retried" while the image is queued
// for that retry; once it has failed the retry is spent and only a Rescan helps.
func FailureCopy(status, code, detail, accountID string) string {
	if status == StatusFailed && code == CodeTimeout {
		return "The scan did not finish in time, even with a larger scanner task. Rescan to try again."
	}
	return FailureAction(code, detail, accountID)
}

// FailureAction is the operator-facing copy for a failure code.
func FailureAction(code, detail, accountID string) string {
	switch code {
	case CodePullDenied:
		return "The harbormaster-scan role in account " + accountID + " is missing, or the repository policy denies pulls."
	case CodeUnsupportedRegistry:
		return "Only Amazon ECR private images are scanned in this version."
	case CodeImageTooLarge:
		return "The image is larger than the scanner limit (HM_SCAN_MAX_IMAGE_MB)."
	case CodeTimeout:
		return "The scan timed out; it is retried once with a larger scanner task."
	case CodeRateLimited:
		return "AWS throttled the pull; the scan is retried."
	case CodeTaskFailed:
		return "The scanner task failed: " + detail
	case CodeTaskMissing:
		return "The scanner task disappeared before it finished; the scan is retried."
	case CodeResultTooLarge:
		return "The scanner produced more output than Harbormaster accepts."
	case CodeDBStale:
		return "Vulnerability data is too old to match against; waiting for a refresh."
	}
	return ""
}
