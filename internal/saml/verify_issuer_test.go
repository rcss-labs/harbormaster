package saml_test

import (
	"testing"
	"time"

	"github.com/beevik/etree"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
)

// crewjam reads IssueInstant and Issuer namespace-blind with the last match
// winning, so both are read from the assertion namespace by Harbormaster.
func TestVerify_IssuerAndIssueInstantReadFromAssertionNamespace(t *testing.T) {
	old := func(el *etree.Element) {
		el.CreateAttr("IssueInstant", time.Now().Add(-30*time.Minute).UTC().Format(time.RFC3339))
	}
	t.Run("foreign_issue_instant_cannot_refresh_an_old_assertion", func(t *testing.T) {
		eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
			h.wantReason(h.respond(tm(fakeidp.Tamper{EditAssertion: func(el *etree.Element) {
				old(el)
				el.CreateAttr("xmlns:x", foreignNS)
				el.CreateAttr("x:IssueInstant", time.Now().UTC().Format(time.RFC3339))
			}})), saml.ReasonValidatorDisagreement)
		})
	})
	t.Run("old_assertion_control", func(t *testing.T) {
		eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
			wantRefused(t, h, h.respond(tm(fakeidp.Tamper{EditAssertion: old})))
		})
	})
	t.Run("duplicate_issuer_is_refused", func(t *testing.T) {
		eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
			h.wantReason(h.respond(tm(fakeidp.Tamper{EditAssertion: func(el *etree.Element) {
				el.InsertChildAt(kids(el, "Issuer")[0].Index(), samlEl("Issuer", "https://other-idp.example"))
			}})), saml.ReasonIssuer)
		})
	})
}
