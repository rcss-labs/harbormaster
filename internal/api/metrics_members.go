package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/store"
)

// memberLiveWindow is how recent a heartbeat must be for a process to count as a live member (the lease timing's
// MemberLive).
const memberLiveWindow = 30 * time.Second

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// memberMetrics renders what the database knows about the split: one line per live member and per lease. It runs at
// scrape time on a web task, which is the one place that sees every process. Any database error returns "" so the
// series are left out rather than reported wrong.
func (s *Server) memberMetrics(ctx context.Context) string {
	members, err := s.Store.LiveMembers(ctx, memberLiveWindow)
	if err != nil {
		return ""
	}
	leases, err := s.Store.ListLeases(ctx)
	if err != nil {
		return ""
	}
	var b strings.Builder
	family := func(name, help string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name) }
	memberLabels := func(m store.Member) string {
		return fmt.Sprintf(`{instance="%s",roles="%s"}`, labelEscaper.Replace(m.InstanceID.String()), labelEscaper.Replace(strings.Join(m.Roles, ",")))
	}
	for _, f := range []struct {
		name, help string
		value      func(store.Member) string
	}{
		{"harbormaster_poll_member_slices", "Slices each live member owns, from its heartbeat.", func(m store.Member) string { return strconv.Itoa(m.Slices) }},
		{"harbormaster_poll_member_weight", "Weight (clusters) of the slices each live member owns.", func(m store.Member) string { return strconv.Itoa(m.Weight) }},
		{"harbormaster_poll_member_focus_objects", "Focused objects each live member serves.", func(m store.Member) string { return strconv.Itoa(m.FocusObjects) }},
		{"harbormaster_poll_member_heartbeat_age_seconds", "Seconds since each live member's last heartbeat.", func(m store.Member) string {
			return strconv.FormatFloat(m.Age.Seconds(), 'f', 1, 64)
		}},
	} {
		family(f.name, f.help)
		for _, m := range members {
			fmt.Fprintf(&b, "%s%s %s\n", f.name, memberLabels(m), f.value(m))
		}
	}
	family("harbormaster_lease_epoch", "Epoch of each lease; instance is the holder, empty when the lease is free.")
	for _, l := range leases {
		instance := ""
		if !l.Free {
			instance = l.OwnerID.String()
		}
		fmt.Fprintf(&b, `harbormaster_lease_epoch{kind="%s",account="%s",region="%s",instance="%s"} %d`+"\n",
			labelEscaper.Replace(l.Key.Kind), labelEscaper.Replace(l.Key.AccountID), labelEscaper.Replace(l.Key.Region), instance, l.Epoch)
	}
	return b.String()
}
