// Package saml holds the SAML 2.0 service-provider logic for Harbormaster:
// SP key generation and at-rest encryption, IdP metadata parsing and safe
// fetching, and SP metadata rendering. It never imports internal/store and
// never logs or returns key material or certificate PEM in error text.
package saml
