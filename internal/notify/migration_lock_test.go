package notify

import (
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// The delivery-table migration must give up on its lock before a sender's result write times out: that write queues
// behind the waiting ALTER, and a timed-out write means the delivered alert is sent again.
func TestDiagnosisMigrationLockTimeoutIsBelowTheWriteTimeout(t *testing.T) {
	body, err := os.ReadFile("../store/migrations/0220_notification_diagnosis.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^set local lock_timeout = '(\d+)(ms|s)';`).FindSubmatch(body)
	if m == nil {
		t.Fatal("the migration sets no lock_timeout")
	}
	n, _ := strconv.Atoi(string(m[1]))
	timeout := time.Duration(n) * time.Second
	if string(m[2]) == "ms" {
		timeout = time.Duration(n) * time.Millisecond
	}
	if timeout*2 > writeTimeout {
		t.Fatalf("lock_timeout = %v, want at most half of the %v write timeout", timeout, writeTimeout)
	}
}
