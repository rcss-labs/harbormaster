// Package execbridge bridges a browser terminal to an ECS Exec session by
// speaking the SSM Session Manager data-channel protocol directly.
//
// It is a from-source port of the parts of github.com/aws/session-manager-plugin
// (Apache-2.0, see THIRD_PARTY_NOTICES.md) needed for a standard interactive
// shell: the binary client-message wire format, the handshake payloads and the
// data-channel acknowledgement/retransmission logic.
//
// Deliberately not ported: KMS-encrypted sessions (encryption challenge and
// KMS handshake actions are reported as unsupported), port-forwarding
// sessions, ResumeSession reconnects, and Windows/stty terminal handling.
package execbridge
