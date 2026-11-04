.DEFAULT_GOAL := lint

GOLANGCI_LINT ?= golangci-lint

.PHONY: lint lint-fix

lint:
	$(GOLANGCI_LINT) run -v ./...

lint-fix:
	$(GOLANGCI_LINT) run -v --fix ./...
