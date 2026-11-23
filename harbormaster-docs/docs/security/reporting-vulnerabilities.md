# Reporting vulnerabilities

This page is the Harbormaster security policy. It tells you how to report a vulnerability privately and what to expect.

## How to report

Report vulnerabilities privately through GitHub private vulnerability reporting:

1. Open the repository at <https://github.com/rcss-labs/harbormaster>.
2. Go to the **Security** tab and choose **Report a vulnerability**. You can also go directly to <https://github.com/rcss-labs/harbormaster/security/advisories/new>.
3. Describe the issue.

Do not open a public issue, discussion or pull request for a suspected vulnerability.

## What to include

- The affected version or commit.
- The component (for example API, SSO, terminal, MCP gateway, Terraform module).
- Steps to reproduce, or a proof of concept.
- The impact you expect.
- Any suggested fix.

Do not include real credentials or other people's data.

## Supported versions

Security fixes are made on the latest release on `main`. Older releases are not patched. Upgrade to the latest release to get fixes. See [Upgrading](../install/upgrading.md).

## What to expect

- We aim to acknowledge a report within a few business days.
- We will confirm whether we can reproduce it and keep you updated in the advisory thread.
- We follow coordinated disclosure. We fix the issue, publish a release, then publish a security advisory. We ask that you do not disclose details publicly until the fix is released. We will credit you in the advisory if you want.

Harbormaster is maintained by a small team, so timelines depend on severity and complexity. We will tell you if a fix will take longer than expected.

## Scope

In scope: the Harbormaster server, web app, MCP gateway, scanner, and the Terraform modules in this repository.

Out of scope: vulnerabilities in AWS services, in your IdP, or in third-party software that you should report to its own maintainers. If a dependency issue affects Harbormaster, report it to us as well.

## Related pages

- [Security model](security-model.md)
- [Hardening checklist](hardening.md)
- [Support](../community/support.md)
