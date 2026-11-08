package scan

import "testing"

func TestFailureAction(t *testing.T) {
	if got := FailureAction(CodePullDenied, "", "123456789012"); got != "The harbormaster-scan role in account 123456789012 is missing, or the repository policy denies pulls." {
		t.Errorf("pull_denied copy: %q", got)
	}
	if got := FailureAction(CodeTaskFailed, "boom", ""); got != "The scanner task failed: boom" {
		t.Errorf("task_failed copy: %q", got)
	}
}
