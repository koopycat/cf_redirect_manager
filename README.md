# cf-redirect

A focused Go CLI and terminal UI for safely managing one Cloudflare Bulk Redirect List. It lists and searches redirects, plans additions and edits, imports CSV upserts, and deletes only explicitly selected item IDs.

## Safety model

- Every mutation is validated, previewed, and confirmed. Scripts must use `--yes` or `--dry-run`.
- CSV import only adds or updates; omitted rows are never deleted. There is no `--sync` mode.
- Updates are explicit DELETE-then-POST operations, with Cloudflare bulk-operation polling between dependent phases.
- The replace-all `PUT /items` endpoint is never used.
- Existing comments and redirect options, including explicit `false` boolean values, are preserved on edits.
- The TUI requires `y` to apply a plan. Pressing `q` or `Esc` during apply stops waiting locally; an operation already submitted to Cloudflare may still finish, so reopen or run `list` to verify.

## Install

Install on macOS or Linux with Homebrew:

```sh
brew install koopycat/tap/cf-redirect
```

Prebuilt archives for Linux and macOS (`amd64` and `arm64`) are available on the [GitHub Releases](https://github.com/koopycat/cf_redirect_manager/releases) page. Each release includes a `SHA256SUMS` file.

To install from source, Go 1.25 or newer is required:

```sh
go install github.com/koopycat/cf-redirect/cmd/cf-redirect@latest
# or from this checkout
just build
```

Maintainers publish a release by pushing a stable semantic-version tag such as `v1.2.3` on a commit reachable from `main`. The release workflow tests the repository, builds all supported platform archives, generates checksums, publishes the tag's GitHub release with generated notes, and updates the formula version and Linux/macOS checksums in `koopycat/homebrew-tap`.

Homebrew publishing uses a dedicated GitHub App installed only on `homebrew-tap`. Configure the `HOMEBREW_APP_ID` and `HOMEBREW_APP_PRIVATE_KEY` Actions repository secrets before publishing a tag. Give the app read and write access to repository contents and no other optional repository or organization permissions. Protect `v*` tags so only release maintainers can create, update, or delete them; protect both repositories' `main` branches from force pushes and deletion. The workflow pins every action to a reviewed commit and restricts the generated installation token to contents access on `homebrew-tap`.

## Configuration

From a Bulk Redirects dashboard URL such as `https://dash.cloudflare.com/YOUR_ACCOUNT_ID/example.com/rules/settings/bulk-redirects/redirect-list/YOUR_LIST_ID/add-redirects`, the account ID is the first 32-hex segment and the list ID is the last one (the zone in the middle is only dashboard navigation context — Bulk Redirect Lists are account-level resources).

Persist the IDs in the OS user config directory (`$XDG_CONFIG_HOME/cf-redirect/config.json` on supported Unix systems):

```sh
cf-redirect config set \
  --account-id YOUR_ACCOUNT_ID \
  --list-id YOUR_LIST_ID
cf-redirect config show
```

The config file is replaced atomically with mode `0600`. Resolution precedence is:

1. `--account-id` / `--list-id`
2. `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_LIST_ID`
3. persisted config file

API token precedence is `CLOUDFLARE_API_TOKEN`, then the OS keychain. Stored credentials are account-specific. Login verifies the token can list the configured redirect list before storing it:

```sh
cf-redirect auth login
# aliases:
cf-redirect login
cf-redirect logout
```

For automation, avoid putting the token in arguments:

```sh
printf '%s' "$TOKEN" | cf-redirect auth login --token-stdin
```

The token needs permission to read and edit the configured account-level Bulk Redirect List.

## Usage

Running `cf-redirect` in an interactive terminal opens the TUI.

```sh
cf-redirect list
cf-redirect list --format json
cf-redirect search example.com
cf-redirect add example.com/blog/ https://www.example.com/articles/ --dry-run
cf-redirect add example.com/blog/ https://www.example.com/articles/ --yes
cf-redirect edit example.com/blog/ https://www.example.com/new-blog/ --yes
cf-redirect delete example.com/blog/ --yes
cf-redirect import redirects.csv --dry-run
cf-redirect status OPERATION_ID
```

Cloudflare permits a source URL without a scheme, such as `example.com/blog/`; this matches both HTTP and HTTPS. Targets must remain absolute `http://` or `https://` URLs. Sources and targets reject fragments, user information, and control characters.

### CSV import

CSV input must have exactly this header:

```csv
source,target
example.com/old/,https://www.example.com/new/
```

Import performs source-keyed upserts. It does not delete current redirects absent from the file.

## Development

The reproducible development shell provides Go 1.25 and `just`:

```sh
direnv allow                 # automatically enter the shell in this checkout
# or without direnv:
devenv shell -- just check
```

Inside the shell:

```sh
just check              # formatting check, tests, vet
just race               # race detector
just integration-mock   # full lifecycle against an in-process HTTP API
just build
```

### Integration tests

`just integration-mock` runs a deterministic redirect lifecycle through the real planner, executor, asynchronous-operation polling, and HTTP client. It uses Go's in-process `httptest.Server`, so normal tests and CI need no credentials, network access, container runtime, or separate MockServer process.

A second test runs the same lifecycle against Cloudflare. It creates a redirect with every option enabled, reads it back, replaces its target while verifying that options and its comment survive, and deletes it. The test is deliberately opt-in and uses the same account and list resolution as the CLI: `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_LIST_ID` override the persisted configuration.

The live test snapshots all pre-existing redirects, mutates only a cryptographically unique test source, verifies the pre-existing entries after each phase, and performs source-scoped cleanup on failure. A dedicated disposable list is still preferable, but the test can temporarily run against the currently configured list without requiring it to be empty.

Give the token read and edit permission for the configured list. Supply it through `CLOUDFLARE_API_TOKEN`, or store it in the account-specific OS keychain with `cf-redirect auth login`, then run:

```sh
just integration-live
```

`just integration-live` sets `CF_REDIRECT_INTEGRATION=1` only for that invocation. Both integration-test recipes run Go in verbose mode and print each lifecycle phase, executor operation, verification, and safety-cleanup step while it happens. If Cloudflare remains unavailable beyond the operation timeout, inspect the configured list and remove any source beginning with `cf-redirect-` before retrying.

An external service such as MockServer or WireMock would be useful if several languages needed to share a standalone Cloudflare simulation or if recorded HTTP fixtures were required. For this Go-only client, `httptest.Server` is the smaller and safer default: the mock starts with the test, binds an ephemeral local port, runs in CI without Docker or Java, and still validates authentication, paths, methods, request bodies, response envelopes, and mutation ordering. The opt-in live test covers drift between that contract and Cloudflare's real API.

## License

Licensed under the [MIT License](LICENSE).
