package saml_test

import (
	"testing"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
)

// The library matches NameID in any namespace, so a signed <x:NameID> inside
// Subject must never become the identity (GO-2026-6909, issue 2).
func TestVerify_ForeignNamespaceNameID(t *testing.T) {
	t.Run("only_foreign_is_no_nameid", func(t *testing.T) {
		h := newHarness(t)
		h.wantReason(h.respond(fakeidp.Tamper{OmitNameID: true, ForeignNameID: "admin@example.test"}), saml.ReasonNoNameID)
	})
	t.Run("only_foreign_encrypted_is_no_nameid", func(t *testing.T) {
		h := newHarness(t)
		h.wantReason(h.respond(fakeidp.Tamper{OmitNameID: true, ForeignNameID: "admin@example.test", EncryptFor: h.active.Cert}), saml.ReasonNoNameID)
	})
	t.Run("both_with_different_values_is_refused", func(t *testing.T) {
		h := newHarness(t)
		h.wantReason(h.respond(fakeidp.Tamper{ForeignNameID: "admin@example.test"}), saml.ReasonValidatorDisagreement)
	})
	t.Run("both_encrypted_with_different_values_is_refused", func(t *testing.T) {
		h := newHarness(t)
		h.wantReason(h.respond(fakeidp.Tamper{ForeignNameID: "admin@example.test", EncryptFor: h.active.Cert}), saml.ReasonValidatorDisagreement)
	})
	t.Run("normal_assertion_unchanged", func(t *testing.T) {
		h := newHarness(t)
		for _, tm := range []fakeidp.Tamper{{}, {EncryptFor: h.active.Cert}} {
			id, err := h.verify(h.respond(tm))
			if err != nil {
				t.Fatal(err)
			}
			if id.NameID != "alex@example.test" || id.NameIDFormat != "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress" {
				t.Fatalf("identity = %q / %q", id.NameID, id.NameIDFormat)
			}
		}
	})
}

// A signed assertion without an Issuer must fail sign-in with a reason and no
// escaping panic (GO-2026-6909, issue 1).
func TestVerify_AssertionWithoutIssuerFailsCleanly(t *testing.T) {
	h := newHarness(t)
	b64 := h.respond(fakeidp.Tamper{OmitAssertionIssuer: true})
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic escaped Verify: %v", r)
			}
		}()
		_, err = h.verify(b64)
	}()
	if err == nil || saml.ReasonOf(err) == saml.ReasonInternal {
		t.Fatalf("err = %v, want a clean rejection reason", err)
	}
	t.Logf("reason = %s", saml.ReasonOf(err))
}
