package saml_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/xmlenc"

	"github.com/rcss-labs/harbormaster/internal/saml"
	"github.com/rcss-labs/harbormaster/internal/saml/fakeidp"
)

// These tests build real IdP-signed assertions with extra or foreign-namespace
// elements. crewjam matches many element and attribute names in any namespace
// (GO-2026-6909), so every field Harbormaster trusts is read from the XML in the
// assertion namespace and compared with what the library parsed.

const (
	foreignNS    = "urn:example:foreign"
	xencNS       = "http://www.w3.org/2001/04/xmlenc#"
	persistentID = "urn:oasis:names:tc:SAML:2.0:nameid-format:persistent"
)

// kids returns the child elements with the given local tag, whatever their namespace.
func kids(el *etree.Element, tag string) []*etree.Element {
	var out []*etree.Element
	for _, c := range el.ChildElements() {
		if c.Tag == tag {
			out = append(out, c)
		}
	}
	return out
}

func samlEl(name, text string) *etree.Element {
	e := etree.NewElement("saml:" + name)
	e.SetText(text)
	return e
}

func foreignEl(name, text string) *etree.Element {
	e := etree.NewElement("x:" + name)
	e.CreateAttr("xmlns:x", foreignNS)
	e.SetText(text)
	return e
}

func subjectOf(el *etree.Element) *etree.Element { return kids(el, "Subject")[0] }

type signMode struct {
	name         string
	omitResponse bool
}

// signModes: the Response and Assertion both signed, or only the Assertion
// (the case where the Response signature does not cover the assertion bytes).
var signModes = []signMode{{"response_and_assertion_signed", false}, {"assertion_only_signed", true}}

func (m signMode) apply(tm fakeidp.Tamper) fakeidp.Tamper {
	tm.OmitResponseSignature = m.omitResponse
	return tm
}

// eachShape runs f for every combination of signing mode and (plain|encrypted).
func eachShape(t *testing.T, f func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper)) {
	for _, m := range signModes {
		for _, enc := range []bool{false, true} {
			name := m.name
			if enc {
				name += "/encrypted"
			}
			t.Run(name, func(t *testing.T) {
				h := newHarness(t)
				f(t, h, func(tm fakeidp.Tamper) fakeidp.Tamper {
					tm = m.apply(tm)
					if enc {
						tm.EncryptFor = h.active.Cert
					}
					return tm
				})
			})
		}
	}
}

func wantAlex(t *testing.T, h *harness, b64 string) saml.Identity {
	t.Helper()
	id, err := h.verify(b64)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if id.NameID != "alex@example.test" || id.NameIDFormat != string(crewjam.EmailAddressNameIDFormat) {
		t.Fatalf("identity = %q / %q", id.NameID, id.NameIDFormat)
	}
	return id
}

func wantRefused(t *testing.T, h *harness, b64 string) {
	t.Helper()
	id, err := h.verify(b64)
	if err == nil {
		t.Fatalf("Verify accepted identity %q (%q)", id.NameID, id.NameIDFormat)
	}
	t.Logf("refused: %v", err)
	if saml.ReasonOf(err) == saml.ReasonInternal {
		t.Fatalf("refused with internal error: %v", err)
	}
}

func TestFakeIdP_EditedAssertionHasExactlyOneSignature(t *testing.T) {
	h := newHarness(t)
	b64 := h.respond(fakeidp.Tamper{ForeignNameID: "x@example.test", OmitResponseSignature: true})
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(raw); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range doc.Root().FindElements("//Signature") {
		if s.NamespaceURI() == "http://www.w3.org/2000/09/xmldsig#" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d ds:Signature elements, want exactly 1 (OmitResponseSignature leaves only the assertion's)", n)
	}
}

func TestVerify_NameIDNamespaceHandling(t *testing.T) {
	foreignAfterReal := func(el *etree.Element) {
		s := subjectOf(el)
		s.InsertChildAt(kids(s, "NameID")[0].Index()+1, foreignEl("NameID", "admin@example.test"))
	}
	cases := []struct {
		name    string
		edit    func(el *etree.Element)
		omit    bool // drop the real NameID
		foreign string
		reason  string // "" means accepted as alex@example.test
		any     bool   // any refusal will do
	}{
		{name: "normal"},
		{name: "only_foreign", omit: true, foreign: "admin@example.test", reason: saml.ReasonNoNameID},
		{name: "foreign_after_real_differs", edit: foreignAfterReal, reason: saml.ReasonValidatorDisagreement},
		{name: "foreign_before_real_keeps_real_identity", edit: func(el *etree.Element) {
			subjectOf(el).InsertChildAt(0, foreignEl("NameID", "admin@example.test"))
		}},
		{name: "two_assertion_namespace_nameids", edit: func(el *etree.Element) {
			s := subjectOf(el)
			s.InsertChildAt(kids(s, "NameID")[0].Index()+1, samlEl("NameID", "admin@example.test"))
		}, reason: saml.ReasonNoNameID},
		{name: "foreign_prefixed_format_attribute", edit: func(el *etree.Element) {
			n := kids(subjectOf(el), "NameID")[0]
			n.CreateAttr("xmlns:x", foreignNS)
			n.CreateAttr("x:Format", persistentID)
		}, reason: saml.ReasonValidatorDisagreement},
		{name: "two_subjects", edit: func(el *etree.Element) {
			s := subjectOf(el)
			el.InsertChildAt(s.Index()+1, s.Copy())
		}, reason: saml.ReasonMalformed},
		{name: "nameid_inside_foreign_subject", edit: func(el *etree.Element) {
			s := subjectOf(el)
			s.Space = "x"
			s.CreateAttr("xmlns:x", foreignNS)
		}, any: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
				b64 := h.respond(tm(fakeidp.Tamper{OmitNameID: c.omit, ForeignNameID: c.foreign, EditAssertion: c.edit}))
				switch {
				case c.any:
					wantRefused(t, h, b64)
				case c.reason == "":
					wantAlex(t, h, b64)
				default:
					h.wantReason(b64, c.reason)
				}
			})
		})
	}
}

// Splitting a NameID with a comment must not let two parsers read two values.
func TestVerify_CommentInNameIDIsRefused(t *testing.T) {
	for _, m := range signModes {
		t.Run(m.name, func(t *testing.T) {
			h := newHarness(t)
			b64 := mutate(t, h.respond(m.apply(fakeidp.Tamper{})), func(root *etree.Element) {
				n := root.FindElement("//NameID")
				n.SetText("alex")
				n.AddChild(etree.NewComment("split"))
				n.AddChild(etree.NewText("@example.test"))
			})
			h.wantReason(b64, saml.ReasonNoNameID)
		})
	}
}

func TestVerify_ForeignEncryptedDataShadowIsIgnored(t *testing.T) {
	shadow := func(t *testing.T, h *harness, nameID string) *etree.Element {
		forged := `<saml:Assertion xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="forged"><saml:Subject><saml:NameID>` + nameID + `</saml:NameID></saml:Subject></saml:Assertion>`
		enc := xmlenc.OAEP()
		enc.BlockCipher = xmlenc.AES128CBC
		enc.DigestMethod = &xmlenc.SHA1
		el, err := enc.Encrypt(h.active.Cert, []byte(forged), nil)
		if err != nil {
			t.Fatal(err)
		}
		el.Space = "x"
		el.CreateAttr("xmlns:x", foreignNS)
		el.CreateAttr("xmlns:xenc", xencNS)
		el.CreateAttr("xmlns:ds", "http://www.w3.org/2000/09/xmldsig#")
		return el
	}
	put := func(t *testing.T, b64 string, el *etree.Element) string {
		return mutate(t, b64, func(root *etree.Element) { kids(root, "EncryptedAssertion")[0].InsertChildAt(0, el) })
	}
	t.Run("foreign_only_nameid_with_matching_shadow", func(t *testing.T) {
		h := newHarness(t)
		b64 := h.respond(fakeidp.Tamper{OmitNameID: true, ForeignNameID: "admin@example.test", EncryptFor: h.active.Cert, OmitResponseSignature: true})
		h.wantReason(b64, saml.ReasonNoNameID) // refused without the shadow
		wantRefused(t, h, put(t, b64, shadow(t, h, "admin@example.test")))
	})
	t.Run("real_assertion_never_yields_the_shadow_identity", func(t *testing.T) {
		h := newHarness(t)
		b64 := h.respond(fakeidp.Tamper{EncryptFor: h.active.Cert, OmitResponseSignature: true})
		id, err := h.verify(put(t, b64, shadow(t, h, "victim@example.test")))
		if err == nil && id.NameID != "alex@example.test" {
			t.Fatalf("accepted the shadow identity %q", id.NameID)
		}
	})
}

// A signed assertion that lacks elements crewjam dereferences must fail with a
// reason and no panic escaping Verify (GO-2026-6909, issue 1).
func TestVerify_SignedAssertionMissingElementsFailsCleanly(t *testing.T) {
	cases := map[string]fakeidp.Tamper{
		"no_issuer":     {OmitAssertionIssuer: true},
		"no_subject":    {EditAssertion: func(el *etree.Element) { el.RemoveChild(subjectOf(el)) }},
		"no_conditions": {EditAssertion: func(el *etree.Element) { el.RemoveChild(kids(el, "Conditions")[0]) }},
	}
	for name, base := range cases {
		t.Run(name, func(t *testing.T) {
			eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
				b64 := h.respond(tm(base))
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic escaped Verify: %v", r)
					}
				}()
				wantRefused(t, h, b64)
			})
		})
	}
}

func spEntity() string { return saml.SPEntityID(testBaseURL) }

func audienceRestrictions(el *etree.Element) []*etree.Element {
	return kids(kids(el, "Conditions")[0], "AudienceRestriction")
}

func TestVerify_AudienceRestrictions(t *testing.T) {
	t.Run("several_audiences_in_one_restriction_with_the_sp_last", func(t *testing.T) {
		eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
			wantAlex(t, h, h.respond(tm(fakeidp.Tamper{EditAssertion: func(el *etree.Element) {
				r := audienceRestrictions(el)[0]
				r.InsertChildAt(0, samlEl("Audience", "https://other.example.test/metadata"))
			}})))
		})
	})
	// crewjam keeps only the last Audience, so isolate the second layer.
	assertionOf := func(root *etree.Element) *etree.Element { return kids(root, "Assertion")[0] }
	second := func(t *testing.T, edit func(restriction *etree.Element, conditions *etree.Element)) (*harness, string) {
		h := newHarness(t)
		h.v.SetFirstLayerForTest(lenientLayer())
		return h, mutate(t, h.respond(fakeidp.Tamper{}), func(root *etree.Element) {
			c := kids(assertionOf(root), "Conditions")[0]
			edit(kids(c, "AudienceRestriction")[0], c)
		})
	}
	t.Run("sp_first_among_several_audiences", func(t *testing.T) {
		h, b64 := second(t, func(r, _ *etree.Element) { r.AddChild(samlEl("Audience", "https://other.example.test/metadata")) })
		if _, err := h.verify(b64); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("every_restriction_must_name_the_sp", func(t *testing.T) {
		h, b64 := second(t, func(r, c *etree.Element) {
			other := etree.NewElement("saml:AudienceRestriction")
			other.AddChild(samlEl("Audience", "https://other.example.test/metadata"))
			c.InsertChildAt(r.Index()+1, other)
		})
		h.wantReason(b64, saml.ReasonValidatorDisagreement) // shared rule: the library layer is bypassed here
	})
	t.Run("foreign_restriction_is_ignored", func(t *testing.T) {
		h, b64 := second(t, func(r, c *etree.Element) {
			f := foreignEl("AudienceRestriction", "")
			f.AddChild(samlEl("Audience", "https://other.example.test/metadata"))
			c.InsertChildAt(r.Index()+1, f)
		})
		if _, err := h.verify(b64); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("only_a_foreign_restriction_is_no_restriction", func(t *testing.T) {
		h, b64 := second(t, func(r, c *etree.Element) {
			f := foreignEl("AudienceRestriction", "")
			f.AddChild(samlEl("Audience", spEntity()))
			c.InsertChildAt(r.Index(), f)
			c.RemoveChild(r)
		})
		h.wantReason(b64, saml.ReasonAudience)
	})
}

func TestVerify_SubjectConfirmationReadFromAssertionNamespace(t *testing.T) {
	scd := func(el *etree.Element) *etree.Element {
		sc := kids(subjectOf(el), "SubjectConfirmation")[0]
		return kids(sc, "SubjectConfirmationData")[0]
	}
	cases := map[string]struct {
		edit   func(el *etree.Element)
		reason string
	}{
		"two_confirmation_data": {edit: func(el *etree.Element) {
			d := scd(el)
			d.Parent().InsertChildAt(d.Index()+1, d.Copy())
		}, reason: saml.ReasonRecipient},
		"foreign_prefixed_recipient_overrides": {edit: func(el *etree.Element) {
			d := scd(el)
			d.CreateAttr("xmlns:x", foreignNS)
			d.CreateAttr("x:Recipient", "https://evil.example/api/auth/saml/acs")
		}, reason: saml.ReasonRecipient}, // crewjam reads the prefixed attribute and refuses itself
		"recipient_only_as_foreign_prefixed_attribute": {edit: func(el *etree.Element) {
			d := scd(el)
			v := d.SelectAttrValue("Recipient", "")
			d.RemoveAttr("Recipient")
			d.CreateAttr("xmlns:x", foreignNS)
			d.CreateAttr("x:Recipient", v)
		}, reason: saml.ReasonValidatorDisagreement}, // crewjam accepts the prefixed attribute; we do not
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
				h.wantReason(h.respond(tm(fakeidp.Tamper{EditAssertion: c.edit})), c.reason)
			})
		})
	}
}

func TestVerify_ConditionsReadFromAssertionNamespace(t *testing.T) {
	conditions := func(el *etree.Element) *etree.Element { return kids(el, "Conditions")[0] }
	cases := map[string]struct {
		edit   func(el *etree.Element)
		reason string
	}{
		"two_conditions": {edit: func(el *etree.Element) {
			c := conditions(el)
			el.InsertChildAt(c.Index()+1, c.Copy())
		}, reason: saml.ReasonMalformed},
		"expiry_only_as_foreign_prefixed_attribute": {edit: func(el *etree.Element) {
			c := conditions(el)
			v := c.SelectAttrValue("NotOnOrAfter", "")
			c.RemoveAttr("NotOnOrAfter")
			c.CreateAttr("xmlns:x", foreignNS)
			c.CreateAttr("x:NotOnOrAfter", v)
		}, reason: saml.ReasonExpired},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
				h.wantReason(h.respond(tm(fakeidp.Tamper{EditAssertion: c.edit})), c.reason)
			})
		})
	}
	t.Run("replay_lifetime_is_the_conditions_value", func(t *testing.T) {
		h := newHarness(t)
		want := time.Now().Add(3 * time.Minute).Truncate(time.Second)
		id, err := h.verify(h.respond(fakeidp.Tamper{NotOnOrAfter: &want}))
		if err != nil {
			t.Fatal(err)
		}
		if !id.NotOnOrAfter.Equal(want) {
			t.Fatalf("NotOnOrAfter = %v, want %v", id.NotOnOrAfter, want)
		}
	})
}

func TestVerify_AttributesAndAuthnStatementReadFromAssertionNamespace(t *testing.T) {
	t.Run("foreign_attribute_elements_are_not_extracted", func(t *testing.T) {
		eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
			id := wantAlex(t, h, h.respond(tm(fakeidp.Tamper{EditAssertion: func(el *etree.Element) {
				st := kids(el, "AttributeStatement")[0]
				// a foreign statement, a foreign attribute and a foreign value for "email"
				fs := foreignEl("AttributeStatement", "")
				fa := etree.NewElement("saml:Attribute")
				fa.CreateAttr("Name", "email")
				fa.AddChild(samlEl("AttributeValue", "evil1@example.test"))
				fs.AddChild(fa)
				el.InsertChildAt(st.Index(), fs)
				xa := foreignEl("Attribute", "")
				xa.CreateAttr("Name", "email")
				xa.AddChild(samlEl("AttributeValue", "evil2@example.test"))
				st.AddChild(xa)
				for _, a := range kids(st, "Attribute") {
					if a.SelectAttrValue("Name", "") == "email" && a.NamespaceURI() == "urn:oasis:names:tc:SAML:2.0:assertion" {
						a.AddChild(foreignEl("AttributeValue", "evil3@example.test"))
					}
				}
			}})))
			if got := id.Attributes["email"]; len(got) != 1 || got[0] != "alex@example.test" {
				t.Fatalf("email = %v, want only the assertion-namespace value", got)
			}
		})
	})
	t.Run("foreign_authn_statement_does_not_count", func(t *testing.T) {
		eachShape(t, func(t *testing.T, h *harness, tm func(fakeidp.Tamper) fakeidp.Tamper) {
			h.wantReason(h.respond(tm(fakeidp.Tamper{EditAssertion: func(el *etree.Element) {
				as := kids(el, "AuthnStatement")[0]
				as.Space = "x"
				as.CreateAttr("xmlns:x", foreignNS)
			}})), saml.ReasonNoAuthnStatement)
		})
	})
	t.Run("foreign_authn_statement_is_a_disagreement", func(t *testing.T) {
		h := newHarness(t)
		// crewjam folds the foreign statement into its struct, so the views differ.
		h.wantReason(h.respond(fakeidp.Tamper{EditAssertion: func(el *etree.Element) {
			f := foreignEl("AuthnStatement", "")
			f.CreateAttr("SessionNotOnOrAfter", time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
			el.AddChild(f)
		}}), saml.ReasonValidatorDisagreement)
	})
}
