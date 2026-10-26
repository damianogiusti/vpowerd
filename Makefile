# All targets run Go inside Docker, so no local Go toolchain is needed.
GO_IMAGE := golang:1.23
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO := docker run --rm -v "$(CURDIR)":/src -v vpowerd-gomod:/go/pkg/mod -w /src $(GO_IMAGE)

.PHONY: test build dist image run-mock

test: ## vet + race tests
	$(GO) sh -c 'go vet ./... && go test -race ./...'

build: ## static binaries for amd64 and arm64 in dist/
	$(GO) sh -c 'for arch in amd64 arm64; do \
	  CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath \
	    -ldflags "-s -w -X main.version=$(VERSION)" -o dist/vpowerd-linux-$$arch ./cmd/vpowerd; done'

dist: ## release assets + checksums.txt in dist/ (same script as CI)
	$(GO) sh scripts/dist.sh $(VERSION)

image: ## local Docker image
	docker build --build-arg VERSION=$(VERSION) -t vpowerd:latest .

run-mock: image ## run against a simulated UPS on http://localhost:8080
	docker run --rm -p 8080:8080 -e VPOWERD_MOCK=J -e VPOWERD_ALLOW_CONTROL=true vpowerd:latest
