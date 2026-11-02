package poller

import (
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
)

func TestConfigFrom_CopiesAnalyticsSettings(t *testing.T) {
	cfg := config.Config{
		Retention: config.RetentionConfig{
			AvailabilityDaily: 1 * time.Hour, CapacityDaily: 2 * time.Hour, UtilDaily: 3 * time.Hour,
		},
		Analytics: config.AnalyticsConfig{Util: true},
	}
	got := ConfigFrom(cfg)
	if got.Retention.AvailabilityDaily != time.Hour || got.Retention.CapacityDaily != 2*time.Hour || got.Retention.UtilDaily != 3*time.Hour {
		t.Errorf("daily retentions not copied: %+v", got.Retention)
	}
	if !got.AnalyticsUtil {
		t.Error("AnalyticsUtil = false, want true")
	}
}
