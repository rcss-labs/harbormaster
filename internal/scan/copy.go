package scan

// FailureAction is the operator-facing copy for a failure code.
func FailureAction(code, detail, accountID string) string {
	switch code {
	case CodeRegistryNotConnected:
		return "Connect AWS account " + accountID + " to scan this image."
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
