package main

import (
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
)

// accessRoles picks the managed-account role a process assumes. A poller and a coordinator each use their own role,
// named like the stored one with -poller or -coordinator appended; role all and role web keep the stored role, so a
// single-task install needs no new IAM role. A web task uses the stored role for everything a user does against a
// managed account: deploys, exec sessions, live logs, log search, and manual wake and sleep.
func accessRoles(role config.Role, stored awsx.RoleSource) awsx.RoleSource {
	switch role {
	case config.RolePoller:
		return awsx.SuffixRoles(stored, "-poller")
	case config.RoleCoordinator:
		return awsx.SuffixRoles(stored, "-coordinator")
	}
	return stored
}
