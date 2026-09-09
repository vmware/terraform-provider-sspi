TEST?=$$(go list ./...)
GOFMT_FILES?=$$(find . -name '*.go')
PKG_NAME=sspi
GIT_COMMIT=$$(git rev-list -1 HEAD 2>/dev/null || echo "unknown")
BUILD_PATH=$$(go env GOPATH)
VERSION?=1.0.0
DIST_DIR:=dist

default: build

tools:
	GO111MODULE=on go install -mod=mod github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
	GO111MODULE=on go install -mod=mod github.com/katbyte/terrafmt

build: fmtcheck
	mkdir -p $(DIST_DIR)
	go build -ldflags "-X main.version=$(VERSION) -X main.commit=$(GIT_COMMIT)" -o $(DIST_DIR)/terraform-provider-sspi .

test: fmtcheck
	go test ./... -v -count=1 -parallel=4

testacc: fmtcheck
	GO111MODULE=on TF_ACC=1 go test ./... -v -count=1 -parallel=4

vet:
	@echo "go vet ."
	@go vet $$(go list ./... | grep -v vendor/) ; if [ $$? -eq 1 ]; then \
		echo ""; \
		echo "Vet found suspicious constructs. Please check the reported constructs"; \
		echo "and fix them if necessary before submitting the code for review."; \
		exit 1; \
	fi

fmt:
	gofmt -s -w $(GOFMT_FILES)

fmtcheck:
	@sh -c "'$(CURDIR)/scripts/gofmtcheck.sh'"

errcheck:
	@sh -c "'$(CURDIR)/scripts/errcheck.sh'"

test-unit:
	go test -v ./internal/... -tags=unittest -count=1

generate:
	go generate ./...
	@$(MAKE) docs-lint-fix

docs-lint:
	@echo "==> Checking Markdown docs lint..."
	markdownlint-cli2 "docs/**/*.md" --config ".markdownlint.jsonc"

docs-lint-fix:
	@echo "==> Fixing Markdown docs lint..."
	markdownlint-cli2 "docs/**/*.md" --config ".markdownlint.jsonc" --fix

.PHONY: build test testacc vet fmt fmtcheck errcheck test-unit generate docs-lint docs-lint-fix tools default
