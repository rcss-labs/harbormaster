package saml

import crewjam "github.com/crewjam/saml"

// SetFirstLayerForTest replaces the crewjam layer so tests can exercise the
// independent second layer (and the disagreement path) in isolation.
func (v *Verifier) SetFirstLayerForTest(f func(raw []byte) (*crewjam.Assertion, error)) {
	v.firstLayer = f
}
