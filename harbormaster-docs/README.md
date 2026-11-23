# Harbormaster documentation

The source of the Harbormaster documentation site. Pages are plain Markdown under [`docs/`](docs/index.md) and read fine on GitHub; [`mkdocs.yml`](mkdocs.yml) builds them into a searchable site with [MkDocs Material](https://squidfunk.github.io/mkdocs-material/).

Start reading at [docs/index.md](docs/index.md).

## Preview the site locally

```sh
cd harbormaster-docs
python3 -m venv .venv && . .venv/bin/activate
pip install mkdocs-material
mkdocs serve            # http://127.0.0.1:8000
mkdocs build --strict   # fails on broken links or pages missing from the nav
```

## Contributing to the docs

See [Writing docs](docs/development/writing-docs.md) for the structure and style rules. Every new page must be added to the `nav` in `mkdocs.yml`.
