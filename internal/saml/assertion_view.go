package saml

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/beevik/etree"
	crewjam "github.com/crewjam/saml"
	"github.com/crewjam/saml/xmlenc"
	xrv "github.com/mattermost/xml-roundtrip-validator"
)

const nsXMLEnc = "http://www.w3.org/2001/04/xmlenc#"

// assertionView is what the single assertion says, read from its XML with
// exact namespace matching. crewjam's struct tags match many element and
// attribute names in any namespace (GO-2026-6909), so nothing the sign-in
// decision or the resulting identity depends on comes from crewjam's struct;
// the struct is only compared against this view (see compareWithLibrary).
type assertionView struct {
	id           string
	issuers      int
	issuer       string
	issueInstant time.Time // zero when absent

	subjects      int
	nameIDs       int
	nameIDText    string
	nameIDClean   bool // the NameID holds only character data (no comment or element splits it)
	nameIDFormat  string
	confirmations []confirmationView

	conditions       int
	notBefore        time.Time // zero when absent
	notOnOrAfter     time.Time // zero when absent
	audienceSets     [][]string
	authnStatements  []authnView
	attributeEntries []attributeView
}

type confirmationView struct {
	method       string
	dataElements int
	recipient    string
	inResponseTo string
	notOnOrAfter time.Time // zero when absent
}

type authnView struct{ sessionNotOnOrAfter *time.Time }

type attributeView struct {
	name, friendlyName string
	values             []string
}

// unprefixedAttr returns the value of the attribute with no namespace prefix.
func unprefixedAttr(el *etree.Element, key string) (string, bool) {
	for _, a := range el.Attr {
		if a.Space == "" && a.Key == key {
			return a.Value, true
		}
	}
	return "", false
}

func timeAttr(el *etree.Element, key string) (time.Time, error) {
	raw, ok := unprefixedAttr(el, key)
	if !ok {
		return time.Time{}, nil
	}
	t, good := parseXSDTime(strings.TrimSpace(raw))
	if !good {
		return time.Time{}, fmt.Errorf("%s is not a timestamp", key)
	}
	return t, nil
}

// charData is the concatenated direct character data, as encoding/xml's
// ",chardata" reads it (comments are skipped, child elements are not entered).
func charData(el *etree.Element) string {
	var b strings.Builder
	for _, tok := range el.Child {
		if cd, ok := tok.(*etree.CharData); ok {
			b.WriteString(cd.Data)
		}
	}
	return b.String()
}

// onlyCharData reports whether el has no child other than character data.
func onlyCharData(el *etree.Element) bool {
	for _, tok := range el.Child {
		if _, ok := tok.(*etree.CharData); !ok {
			return false
		}
	}
	return true
}

// readAssertion builds the view from the assertion element. A structural
// problem (a duplicated Subject or Conditions, an unreadable timestamp) is a
// malformed response.
func readAssertion(el *etree.Element) (*assertionView, *finding) {
	malformed := func(format string, args ...any) (*assertionView, *finding) {
		return nil, &finding{reason: ReasonMalformed, detail: fmt.Sprintf(format, args...)}
	}
	v := &assertionView{}
	v.id, _ = unprefixedAttr(el, "ID")
	var err error
	if v.issueInstant, err = timeAttr(el, "IssueInstant"); err != nil {
		return malformed("Assertion: %v", err)
	}
	issuers := childrenNamed(el, nsAssertion, "Issuer")
	v.issuers = len(issuers)
	if len(issuers) == 1 {
		v.issuer = strings.TrimSpace(charData(issuers[0]))
	}

	subjects := childrenNamed(el, nsAssertion, "Subject")
	v.subjects = len(subjects)
	if v.subjects > 1 {
		return malformed("%d Subject elements", v.subjects)
	}
	for _, subject := range subjects {
		names := childrenNamed(subject, nsAssertion, "NameID")
		v.nameIDs = len(names)
		if len(names) == 1 {
			v.nameIDClean = onlyCharData(names[0])
			v.nameIDText = strings.TrimSpace(charData(names[0]))
			v.nameIDFormat, _ = unprefixedAttr(names[0], "Format")
		}
		for _, sc := range childrenNamed(subject, nsAssertion, "SubjectConfirmation") {
			c := confirmationView{}
			c.method, _ = unprefixedAttr(sc, "Method")
			data := childrenNamed(sc, nsAssertion, "SubjectConfirmationData")
			c.dataElements = len(data)
			if len(data) == 1 {
				c.recipient, _ = unprefixedAttr(data[0], "Recipient")
				c.inResponseTo, _ = unprefixedAttr(data[0], "InResponseTo")
				t, err := timeAttr(data[0], "NotOnOrAfter")
				if err != nil {
					return malformed("SubjectConfirmationData: %v", err)
				}
				c.notOnOrAfter = t
			}
			v.confirmations = append(v.confirmations, c)
		}
	}

	conds := childrenNamed(el, nsAssertion, "Conditions")
	v.conditions = len(conds)
	if v.conditions > 1 {
		return malformed("%d Conditions elements", v.conditions)
	}
	for _, c := range conds {
		var err error
		if v.notBefore, err = timeAttr(c, "NotBefore"); err != nil {
			return malformed("Conditions: %v", err)
		}
		if v.notOnOrAfter, err = timeAttr(c, "NotOnOrAfter"); err != nil {
			return malformed("Conditions: %v", err)
		}
		for _, r := range childrenNamed(c, nsAssertion, "AudienceRestriction") {
			var set []string
			for _, aud := range childrenNamed(r, nsAssertion, "Audience") {
				set = append(set, strings.TrimSpace(charData(aud)))
			}
			v.audienceSets = append(v.audienceSets, set)
		}
	}

	for _, st := range childrenNamed(el, nsAssertion, "AuthnStatement") {
		t, err := timeAttr(st, "SessionNotOnOrAfter")
		if err != nil {
			return malformed("AuthnStatement: %v", err)
		}
		a := authnView{}
		if !t.IsZero() {
			a.sessionNotOnOrAfter = &t
		}
		v.authnStatements = append(v.authnStatements, a)
	}

	for _, st := range childrenNamed(el, nsAssertion, "AttributeStatement") {
		for _, attr := range childrenNamed(st, nsAssertion, "Attribute") {
			a := attributeView{}
			a.name, _ = unprefixedAttr(attr, "Name")
			a.friendlyName, _ = unprefixedAttr(attr, "FriendlyName")
			for _, val := range childrenNamed(attr, nsAssertion, "AttributeValue") {
				a.values = append(a.values, charData(val))
			}
			v.attributeEntries = append(v.attributeEntries, a)
		}
	}
	return v, nil
}

// compareWithLibrary fails closed when crewjam parsed something different from
// the namespace-exact view: a disagreement means a foreign-namespace element
// or prefixed attribute was folded into the library's struct.
func (v *assertionView) compareWithLibrary(a *crewjam.Assertion) string {
	if v.id != a.ID {
		return "assertion ID"
	}
	if v.issuer != a.Issuer.Value {
		return "Issuer"
	}
	if !v.issueInstant.Equal(a.IssueInstant) {
		return "IssueInstant"
	}
	if (a.Subject == nil) != (v.subjects == 0) {
		return "Subject"
	}
	if a.Subject != nil {
		if len(a.Subject.SubjectConfirmations) != len(v.confirmations) {
			return "SubjectConfirmation count"
		}
		for i, sc := range a.Subject.SubjectConfirmations {
			mine := v.confirmations[i]
			d := sc.SubjectConfirmationData
			if sc.Method != mine.method || (d == nil) != (mine.dataElements == 0) {
				return "SubjectConfirmation"
			}
			if d != nil && (d.Recipient != mine.recipient || d.InResponseTo != mine.inResponseTo || !d.NotOnOrAfter.Equal(mine.notOnOrAfter)) {
				return "SubjectConfirmationData"
			}
		}
	}
	if (a.Conditions == nil) != (v.conditions == 0) {
		return "Conditions"
	}
	if a.Conditions != nil && (!a.Conditions.NotBefore.Equal(v.notBefore) || !a.Conditions.NotOnOrAfter.Equal(v.notOnOrAfter)) {
		return "Conditions timestamps"
	}
	if len(a.AuthnStatements) != len(v.authnStatements) {
		return "AuthnStatement count"
	}
	return ""
}

// assertionElement returns the XML of the single assertion the library
// validated: the Assertion child of the Response, or the decrypted
// EncryptedAssertion (the Response tree only holds ciphertext for those).
// usedKey is the index in keys of the key the library decrypted with.
func assertionElement(root *etree.Element, keys []SPKey, usedKey int) (*etree.Element, *finding) {
	if as := childrenNamed(root, nsAssertion, "Assertion"); len(as) == 1 && len(childrenNamed(root, nsAssertion, "EncryptedAssertion")) == 0 {
		return as[0], nil
	}
	encrypted := childrenNamed(root, nsAssertion, "EncryptedAssertion")
	if len(encrypted) != 1 || len(childrenNamed(root, nsAssertion, "Assertion")) != 0 {
		return nil, &finding{reason: ReasonMalformed, detail: "no single assertion element"}
	}
	if usedKey < 0 || usedKey >= len(keys) || keys[usedKey].Key == nil {
		return nil, &finding{reason: ReasonDecryptFailed, detail: "no record of the key the library decrypted with"}
	}
	el, err := decryptAssertion(encrypted[0], keys[usedKey])
	if err != nil {
		return nil, &finding{reason: ReasonDecryptFailed, detail: "cannot decrypt the assertion again: " + err.Error()}
	}
	return el, nil
}

// decryptAssertion repeats crewjam's EncryptedAssertion decryption on exactly
// the elements crewjam uses (one EncryptedData and at most one EncryptedKey,
// matched by namespace) with the key it succeeded with. Anything that makes
// crewjam's choice ambiguous is an error, so both layers read the same bytes.
func decryptAssertion(encryptedEl *etree.Element, key SPKey) (*etree.Element, error) {
	data := childrenNamed(encryptedEl, nsXMLEnc, "EncryptedData")
	if len(data) != 1 {
		return nil, fmt.Errorf("%d EncryptedData elements", len(data))
	}
	keyEls := childrenNamed(encryptedEl, nsXMLEnc, "EncryptedKey")
	legacy := encryptedEl.FindElement("./EncryptedKey") // what crewjam looks up, ignoring the namespace
	if len(keyEls) > 1 || (len(keyEls) == 1 && legacy != keyEls[0]) || (len(keyEls) == 0 && legacy != nil) {
		return nil, errors.New("ambiguous EncryptedKey")
	}
	var k interface{} = key.Key
	if len(keyEls) == 1 {
		var err error
		if k, err = xmlenc.Decrypt(key.Key, keyEls[0]); err != nil {
			return nil, err
		}
	}
	plain, err := xmlenc.Decrypt(k, data[0])
	if err != nil {
		return nil, err
	}
	if err := xrv.Validate(bytes.NewReader(plain)); err != nil {
		return nil, fmt.Errorf("plaintext is not round-trip safe XML: %w", err)
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(plain); err != nil {
		return nil, err
	}
	root := doc.Root()
	if root == nil || root.Tag != "Assertion" || root.NamespaceURI() != nsAssertion {
		return nil, errors.New("plaintext is not a SAML Assertion")
	}
	return root, nil
}
