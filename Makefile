default: help

.PHONY: help
help: ## Show this help.
	@fgrep -h "##" $(MAKEFILE_LIST)  | fgrep -v fgrep | sed -e 's/:.*##/:##/' | awk -F':##' '{printf "%-12s %s\n",$$1, $$2}'

.PHONY: build
build: ## Build the project
	go build -o bin/infracost main.go

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: lint_install
lint_install: ## Install golangci-lint
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

.PHONY: lint
lint: lint_install ## Run linting operations
	golangci-lint run ./...

.PHONY: mockery_install
mock_install: ## Install mockery
	go install github.com/vektra/mockery/v3@latest

.PHONY: mocks
mocks: mockery_install ## Generate mocks
	mockery

.PHONY: fmt
fmt: ## Format the code
	@gofmt -l . | while read -r f; do \
		echo "The following files are not formatted correctly:"; \
		gofmt -l .; \
		exit 1; \
	done

.PHONY: benchmark
benchmark: ## Run the benchmarks.
	go test -run=^$$ -bench=. -benchmem ./...

.PHONY: spelling
spelling: ## Run spelling checks
	codespell . --quiet-level=2 --skip="vendor,.codespell,.git,*.json,*.tf,*.tfvars,*.tfvars.json,go.sum,go.mod,.infracost" -L cancelled --check-hidden --builtin en-GB_to_en-US

.PHONY: install
install: build ## Install the binary to $GOPATH/bin
	@if [ -z "$$GOPATH" ]; then \
		echo "Error: GOPATH is not set. Please set GOPATH to the Go workspace directory."; \
		exit 1; \
	fi
	cp bin/infracost ${GOPATH}/bin/infracost