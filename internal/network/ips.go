package network

import (
	"net/netip"
	"strings"
)

// awsReservedIPs are the five addresses AWS reserves in every subnet.
const awsReservedIPs = 5

// SubnetIPs returns used and total usable addresses of a subnet. The
// available count from AWS already excludes task ENIs, so used = total -
// available. Both values are clamped to [0, total]; a bad CIDR yields (0, 0).
func SubnetIPs(cidr string, availableIPs int) (used, total int) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil || !prefix.Addr().Is4() {
		return 0, 0
	}
	total = 1<<(32-prefix.Bits()) - awsReservedIPs
	if total < 0 {
		return 0, 0
	}
	used = total - availableIPs
	if used < 0 {
		used = 0
	}
	if used > total {
		used = total
	}
	return used, total
}

// ShortTaskID is the first 8 characters of the last path segment of a task ARN.
func ShortTaskID(taskARN string) string {
	id := taskARN
	if slash := strings.LastIndex(taskARN, "/"); slash >= 0 {
		id = taskARN[slash+1:]
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}
