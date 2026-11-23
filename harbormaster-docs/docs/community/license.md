# Licence

This page summarises the licence of Harbormaster and its bundled third-party components. It is for users and redistributors.

## Harbormaster

Harbormaster is released under the MIT licence. Copyright RCSS Labs. The full text is in `LICENSE` at the repository root. In short, you may use, copy, modify, merge, publish, distribute, sublicense and sell copies, as long as you keep the copyright and licence notice. The software is provided without warranty.

## Third-party components

Bundled third-party components and their licences are listed in `THIRD_PARTY_NOTICES.md` at the repository root. They include:

- IBM Plex Sans and Geist Mono fonts, under the SIL Open Font License 1.1.
- Parts of the AWS Session Manager plugin, ported into `internal/execbridge`, under Apache-2.0.
- Go and image dependencies such as `crewjam/saml`, `beevik/etree`, `russellhaering/goxmldsig`, `golang-jwt/jwt` and `valkey-io/valkey-go`, and the Syft and Grype tools in the scanner image.

Licence copies for bundled files are in `third_party/licenses/`.

## Keeping notices correct

`tools/check-notices.sh` runs as part of `make lint`. It checks that `LICENSE` is the MIT licence, that files ported from AWS carry the Apache-2.0 header, that the licence copies exist, and that `THIRD_PARTY_NOTICES.md` names every required component. Run it with `make check-notices`.

## Related pages

- [Coding conventions](../development/coding-conventions.md)
- [Contributing](contributing.md)
