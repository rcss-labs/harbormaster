# Writing docs

This page explains how this documentation site is organised and how to preview and change it. It is for anyone editing the docs.

## Structure

The site is MkDocs Material, in `harbormaster-docs/`. Pages are Markdown under `harbormaster-docs/docs/`, grouped by purpose:

| Folder | Purpose |
|---|---|
| `getting-started/` | What Harbormaster is, concepts, architecture, quickstarts |
| `install/` | Prerequisites, images, Terraform, first run, accounts, SSO, split services, MCP gateway, upgrades |
| `guides/` | Task-oriented guides for the web app |
| `operations/` | Running Harbormaster: health, HA, limits, costs, retention, backup, troubleshooting, runbooks |
| `reference/` | Configuration, CLI, API, permissions, metrics, Terraform inputs and outputs, glossary, FAQ |
| `security/` | Security model, IAM, hardening, reporting |
| `development/` | Contributor docs |
| `community/` | Contributing, conduct, governance, support, licence, changelog |

Screenshots are in `assets/screens/`. The logo is `assets/main_logo.png`. Files must read well on GitHub too, so use relative links such as `../reference/configuration.md`.

## Style rules

- Audience: DevOps and platform engineers running ECS.
- Plain, direct English. Short sentences. Active voice. Sentence-case headings.
- One `# Title` per page, followed by a one to three sentence summary of what the page covers and who it is for.
- How-to pages: Before you begin, numbered steps, Check it worked, Next steps.
- Reference pages: complete, exact tables (name, default, description).
- Code blocks carry a language tag. Placeholders look like `<account-id>`. Never use real account IDs, secrets, names or emails.
- Use admonitions (`!!! warning "Title"` with a 4-space indented body) only for real warnings.
- No marketing language and no emojis.
- Do not copy large blocks between pages. Link instead.
- Describe the product as it is. Do not invent flags, variables, defaults or behavior: check the code first. When the code and the README disagree, the code is right.
- Mark anything you cannot verify with `<!-- TODO(docs): verify ... -->`.
- Product name is Harbormaster. "The web app" is the UI. Coordinator, poller and web are "roles".

## Preview

From `harbormaster-docs/`:

```sh
pip install mkdocs-material
mkdocs serve
```

Open the address it prints (usually `http://127.0.0.1:8000`). It rebuilds when you save.

## Check it worked

Open the page you changed, follow its links, and look for warnings in the `mkdocs serve` output about missing files or anchors.

## Next steps

- [Contributing](../community/contributing.md)
