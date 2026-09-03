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

Go 1.25 or newer is required.

```sh
go install github.com/koopycat/cf-redirect/cmd/cf-redirect@latest
# or from this checkout
just build
```

## Configuration

From a Bulk Redirects dashboard URL such as `https://dash.cloudflare.com/2629cf85d55899c0223eb91fbde5490d/vkoop.de/rules/settings/bulk-redirects/redirect-list/dc4ec1437f18495db6fe771e53edb478/add-redirects`, the account ID is the first 32-hex segment and the list ID is the last one (the zone in the middle is only dashboard navigation context — Bulk Redirect Lists are account-level resources).

Persist the IDs in the OS user config directory (`$XDG_CONFIG_HOME/cf-redirect/config.json` on supported Unix systems):

```sh
cf-redirect config set \
  --account-id 2629cf85d55899c0223eb91fbde5490d \
  --list-id dc4ec1437f18495db6fe771e53edb478
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
just check     # formatting check, tests, vet
just race      # race detector
just build
```
