# Support

This page tells you where to get help with Harbormaster and what to include when you ask. It is for users and operators.

## Where to ask

- **Questions and ideas:** open an issue on <https://github.com/rcss-labs/harbormaster/issues>.
- **Bugs and feature requests:** GitHub Issues. Search existing issues first.
- **Security problems:** do not use issues or discussions. Report them privately. See [Reporting vulnerabilities](../security/reporting-vulnerabilities.md).

Before you ask, check [Troubleshooting](../operations/troubleshooting.md) and the [FAQ](../reference/faq.md).

Harbormaster is maintained by a small team, and support is on a best-effort basis.

## What to include

- The Harbormaster version (`harbormaster version`) and how you deploy it (single service or split services, Terraform module version).
- What you expected and what happened.
- Steps to reproduce.
- Relevant log lines from the server log group, with secrets and account IDs removed.
- Settings that matter (`HM_*` variables or Terraform inputs), with secret values removed.
- Browser and version, for UI problems.

Do not paste tokens, passwords, session keys, SAML private keys or other secrets.

## Related pages

- [Contributing](contributing.md)
- [Troubleshooting](../operations/troubleshooting.md)
