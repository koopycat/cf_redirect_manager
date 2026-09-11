#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT HUP INT TERM

cat >"$tmp/cf-redirect" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$FAKE_ARGS"
cat >"$FAKE_STDIN"
[[ "${FAKE_FAIL:-false}" != true ]]
SH
cat >"$tmp/browser" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$1" >"$BROWSER_URL"
SH
chmod +x "$tmp/cf-redirect" "$tmp/browser"

account_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
list_id="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

printf '%s\n' "generated-secret" | \
CF_REDIRECT_BIN="$tmp/cf-redirect" \
CF_REDIRECT_BROWSER="$tmp/browser" \
FAKE_ARGS="$tmp/args" FAKE_STDIN="$tmp/stdin" BROWSER_URL="$tmp/url" \
  "$root/scripts/create-cloudflare-token" \
    --account-id "$account_id" \
    --verify-list-id "$list_id" \
    --name "cf redirect test" \
    --token-stdin >"$tmp/stdout" 2>"$tmp/stderr"

[[ "$(cat "$tmp/stdin")" == "generated-secret" ]]
[[ "$(cat "$tmp/args")" == "--account-id $account_id --list-id $list_id auth login --token-stdin" ]]
url="$(cat "$tmp/url")"
python3 - "$url" "$account_id" <<'PY'
import json
import sys
from urllib.parse import parse_qs, urlparse

url, account_id = sys.argv[1:]
parsed = urlparse(url)
assert parsed.scheme == "https"
assert parsed.netloc == "dash.cloudflare.com"
assert parsed.path == "/profile/api-tokens"
query = parse_qs(parsed.query)
assert json.loads(query["permissionGroupKeys"][0]) == [
    {"key": "account_rule_lists", "type": "edit"}
]
assert query["accountId"] == [account_id]
assert query["zoneId"] == ["all"]
assert query["name"] == ["cf redirect test"]
PY
if grep -q 'generated-secret' "$tmp/stdout" "$tmp/stderr" "$tmp/url" "$tmp/args"; then
  echo "secret leaked outside the cf-redirect stdin pipe" >&2
  exit 1
fi

printf '%s\n' "another-secret" | \
CF_REDIRECT_BIN="$tmp/cf-redirect" \
FAKE_ARGS="$tmp/args" FAKE_STDIN="$tmp/stdin" \
  "$root/scripts/create-cloudflare-token" \
    --account-id "$account_id" --no-open --token-stdin >"$tmp/url-output" 2>"$tmp/stderr"
[[ "$(cat "$tmp/args")" == "--account-id $account_id auth login --token-stdin" ]]
grep -q '^https://dash.cloudflare.com/profile/api-tokens?' "$tmp/url-output"
if grep -q 'another-secret' "$tmp/url-output" "$tmp/stderr"; then
  echo "secret leaked in --no-open mode" >&2
  exit 1
fi

if printf '%s\n' "failing-secret" | \
  CF_REDIRECT_BIN="$tmp/cf-redirect" FAKE_FAIL=true \
  FAKE_ARGS="$tmp/args" FAKE_STDIN="$tmp/stdin" \
  "$root/scripts/create-cloudflare-token" \
    --account-id "$account_id" --no-open --token-stdin >"$tmp/failure-out" 2>"$tmp/failure-err"; then
  echo "expected verification failure" >&2
  exit 1
fi
grep -q 'revoke the token in Cloudflare' "$tmp/failure-err"
if grep -q 'failing-secret' "$tmp/failure-out" "$tmp/failure-err"; then
  echo "secret leaked during failure" >&2
  exit 1
fi

echo "create-cloudflare-token tests passed"
