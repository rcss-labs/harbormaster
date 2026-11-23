# Frontend

This page describes the web app in `web/`: its stack, layout, how to run it against a local server, and how to test and lint it. It is for contributors working on the UI.

## Stack

- React 19 with TypeScript, built by Vite.
- TanStack Query for data fetching, React Router for routing.
- xterm.js for the terminal.
- Fonts IBM Plex Sans and Geist Mono, shipped as files (the CSP blocks `data:` fonts).
- Vitest and Testing Library for unit tests, Playwright for end-to-end tests.
- Node 26 or newer.

The build writes to `internal/web/dist`, which the Go binary embeds.

## Layout

| Path | Contents |
|---|---|
| `web/src/api/` | API client, query hooks, types, polling and focus helpers |
| `web/src/app/` | Pages and features (admin, analytics, auth, clusters, logs, networks, and more) |
| `web/src/components/` | Shared components |
| `web/src/charts/` | Chart components |
| `web/src/styles/` | Styles |
| `web/src/test/` | Vitest setup |
| `web/e2e/` | Playwright specs for `make e2e` (desktop and `.mobile` variants) |
| `web/e2e-floci/` | Playwright specs for the floci harness |
| `web/scripts/` | Build helpers such as icon rendering (`make icons`) |

## Run with hot reload

```sh
make run                       # terminal 1: server on 127.0.0.1:8480
npm --prefix web ci            # first time
npm --prefix web run dev       # terminal 2: Vite dev server
```

Vite proxies `/api` to `http://127.0.0.1:8480`.

## Scripts

| Command | What it does |
|---|---|
| `npm --prefix web run dev` | Vite dev server |
| `npm --prefix web run build` | Type-check, then build into `internal/web/dist` |
| `npm --prefix web test -- --run` | Vitest, once |
| `npm --prefix web run lint` | ESLint |
| `npm --prefix web run typecheck` | `tsc --noEmit` |
| `npm --prefix web run e2e` | Playwright (use `make e2e`, which prepares the database) |

`make lint` runs lint and typecheck, and `make test` runs the Vitest suite.

## Next steps

- [Testing](testing.md)
- [Coding conventions](coding-conventions.md)
