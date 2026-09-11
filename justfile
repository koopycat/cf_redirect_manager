set shell := ["bash", "-euo", "pipefail", "-c"]

fmt:
    gofmt -w $(find . -name '*.go' -not -path './vendor/*')

fmt-check:
    test -z "$(gofmt -l $(find . -name '*.go' -not -path './vendor/*'))"

test:
    go test ./...
    bash scripts/create-cloudflare-token_test.sh

race:
    go test -race ./...

integration-mock:
    go test -count=1 -v -run '^TestRedirectLifecycleAgainstMockCloudflare$' ./integration

integration-live:
    CF_REDIRECT_INTEGRATION=1 go test -count=1 -timeout 12m -v -run '^TestRedirectLifecycleAgainstLiveCloudflare$' ./integration

vet:
    go vet ./...

check: fmt-check test vet

build:
    mkdir -p bin
    go build -o bin/cf-redirect ./cmd/cf-redirect

run *args:
    go run ./cmd/cf-redirect {{args}}
