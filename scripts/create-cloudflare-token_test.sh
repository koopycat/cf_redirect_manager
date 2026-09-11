#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
server_pid=""
cleanup() {
  [[ -z "$server_pid" ]] || kill "$server_pid" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM

cat >"$tmp/server.py" <<'PY'
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

log_path = os.environ["REQUEST_LOG"]

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, status, body):
        encoded = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def record(self, body=None):
        with open(log_path, "a", encoding="utf-8") as log:
            log.write(json.dumps({
                "method": self.command,
                "path": self.path,
                "authorization": self.headers.get("Authorization"),
                "body": body,
            }) + "\n")

    def do_GET(self):
        self.record()
        self.respond(200, {"success": True, "result": [
            {"id": "permission-id", "name": "Account Filter Lists Edit"},
            {"id": "wrong-id", "name": "Account Filter Lists Read"},
        ]})

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(length))
        self.record(body)
        self.respond(200, {"success": True, "result": {"id": "created-id", "value": "generated-secret"}})

    def do_DELETE(self):
        self.record()
        self.respond(200, {"success": True, "result": {"id": "created-id"}})

server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(os.environ["PORT_FILE"], "w", encoding="utf-8") as port_file:
    port_file.write(str(server.server_port))
server.serve_forever()
PY

cat >"$tmp/cf-redirect" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >"$FAKE_ARGS"
cat >"$FAKE_STDIN"
[[ "${FAKE_FAIL:-false}" != true ]]
SH
chmod +x "$tmp/cf-redirect"

REQUEST_LOG="$tmp/requests" PORT_FILE="$tmp/port" python3 "$tmp/server.py" &
server_pid=$!
for _ in $(seq 1 50); do
  [[ -s "$tmp/port" ]] && break
  sleep 0.1
done
[[ -s "$tmp/port" ]] || { echo "mock server did not start" >&2; exit 1; }
api_base="http://127.0.0.1:$(cat "$tmp/port")"
account_id="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
list_id="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

CLOUDFLARE_TOKEN_CREATOR_TOKEN="bootstrap-secret" \
CLOUDFLARE_API_BASE_URL="$api_base" \
CF_REDIRECT_BIN="$tmp/cf-redirect" \
FAKE_ARGS="$tmp/args" FAKE_STDIN="$tmp/stdin" \
  "$root/scripts/create-cloudflare-token" --account-id "$account_id" --name "test token" >"$tmp/stdout"

[[ "$(cat "$tmp/stdin")" == "generated-secret" ]]
[[ "$(cat "$tmp/args")" == "--account-id $account_id auth login --token-stdin" ]]
if grep -q 'generated-secret\|bootstrap-secret' "$tmp/stdout"; then
  echo "secret leaked to stdout" >&2
  exit 1
fi

CLOUDFLARE_TOKEN_CREATOR_TOKEN="bootstrap-secret" \
CLOUDFLARE_API_BASE_URL="$api_base" \
CF_REDIRECT_BIN="$tmp/cf-redirect" \
FAKE_ARGS="$tmp/args" FAKE_STDIN="$tmp/stdin" \
  "$root/scripts/create-cloudflare-token" --account-id "$account_id" --verify-list-id "$list_id" --name "test token 2" >"$tmp/stdout"
[[ "$(cat "$tmp/args")" == "--account-id $account_id --list-id $list_id auth login --token-stdin" ]]

jq -se --arg account "$account_id" '
  length == 4 and
  .[0].method == "GET" and
  .[0].authorization == "Bearer bootstrap-secret" and
  .[1].method == "POST" and
  .[1].body.name == "test token" and
  .[1].body.policies == [{
    effect: "allow",
    resources: {("com.cloudflare.api.account." + $account): "*"},
    permission_groups: [{id: "permission-id"}]
  }] and
  .[2].method == "GET" and
  .[3].method == "POST" and
  .[3].body.name == "test token 2"
' "$tmp/requests" >/dev/null

: >"$tmp/requests"
if CLOUDFLARE_TOKEN_CREATOR_TOKEN="bootstrap-secret" \
  CLOUDFLARE_API_BASE_URL="$api_base" \
  CF_REDIRECT_BIN="$tmp/cf-redirect" FAKE_FAIL=true \
  FAKE_ARGS="$tmp/args" FAKE_STDIN="$tmp/stdin" \
  "$root/scripts/create-cloudflare-token" --account-id "$account_id" --verify-list-id "$list_id" >"$tmp/failure-out" 2>"$tmp/failure-err"; then
  echo "expected verification failure" >&2
  exit 1
fi
jq -se 'length == 3 and .[2].method == "DELETE" and .[2].path == "/accounts/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/tokens/created-id"' "$tmp/requests" >/dev/null
if grep -q 'generated-secret\|bootstrap-secret' "$tmp/failure-out" "$tmp/failure-err"; then
  echo "secret leaked during failure" >&2
  exit 1
fi

echo "create-cloudflare-token tests passed"
