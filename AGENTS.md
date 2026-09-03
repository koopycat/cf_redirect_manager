# Agent instructions

## Purpose

`cf-redirect` is a Go CLI and Charm TUI for safely managing one configured Cloudflare Bulk Redirect List.

## Development environment

Enter commands through `devenv shell -- ...`; direnv activates the same shell automatically after `direnv allow`.

## Commands

- `just fmt` — format Go sources
- `just test` — run tests
- `just vet` — run static checks
- `just check` — format check, tests, and vet
- `just build` — build `./bin/cf-redirect`

Use `go` through the project commands inside the devenv shell. Do not add a Cloudflare SDK for the focused API surface.

## Invariants

- Never persist, print, or log Cloudflare API tokens outside the OS keychain. Environment token precedence is `CLOUDFLARE_API_TOKEN`, then the account-specific keychain entry.
- The configured account and list IDs are explicit on every API operation.
- CSV import only adds or updates. Missing CSV rows never cause deletions. There is no sync mode.
- Never use Cloudflare's replace-all `PUT /items` endpoint.
- Every mutation is planned and shown before apply. Non-interactive mutation requires `--yes` or `--dry-run`.
- Delete requests must always contain explicit item IDs in `{\"items\":[{\"id\":...}]}`.
- Item listing uses Cloudflare cursor pagination (`per_page=500`) and must reject repeated cursors.
- Cloudflare mutations are asynchronous. Poll each operation to completion before submitting a dependent operation.
- Preserve redirect options and comments when editing existing entries.
- Keep domain, planner, and application logic independent from Cobra and Bubble Tea.
