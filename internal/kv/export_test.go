package kv

import "time"

// SetHealthTimings shortens the health constants so tests do not wait for the real ones.
func SetHealthTimings(h *Health, command, pingEvery, pingTimeout time.Duration) {
	h.commandTimeout = command
	h.pingEvery = pingEvery
	h.pingTimeout = pingTimeout
}
