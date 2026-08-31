VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: web build test verify linux

web:
	npm --prefix web ci
	npm --prefix web run build

build: web
	go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sub2api-limit-portal ./cmd/sub2api-limit-portal

test:
	go test ./...
	npm --prefix web run typecheck
	npm --prefix web run test

verify: test web
	go vet ./...

linux: web
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sub2api-limit-portal-linux-amd64 ./cmd/sub2api-limit-portal
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/sub2api-limit-portal-linux-arm64 ./cmd/sub2api-limit-portal
