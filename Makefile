GOLANGCI_LINT_VERSION := v2.15.0

# go.mod pins the toolchain; auto lets go fetch it even where the
# environment says GOTOOLCHAIN=local. golangci-lint inherits it when it
# shells out to go.
export GOTOOLCHAIN := auto

BIN       := $(CURDIR)/bin
KEY_TOOL  := $(BIN)/tofu-sshagent-key
LINT      := $(BIN)/golangci-lint
RECIPIENTS := encryption/recipients.json

# The stack to plan or apply: abrosimov or abrosimov-tech.
OWNER ?= abrosimov
STACK := stacks/$(OWNER)

# The organisation API refuses a settings update without the billing email,
# and the email must not be committed to this public repository.
ifeq ($(OWNER),abrosimov-tech)
export TF_VAR_billing_email = $(shell gh api orgs/abrosimov-tech --jq .billing_email)
endif

.PHONY: build test vet lint fmt fmt-check validate check init plan apply key-init key-enroll key-list

build:
	go build -o $(KEY_TOOL) ./cmd/tofu-sshagent-key

test:
	go test -tags unit ./...

vet:
	go vet -tags unit ./...

# Built with the toolchain go.mod pins, because a golangci-lint built with
# an older Go refuses to lint code that targets a newer one.
$(LINT):
	GOBIN=$(BIN) go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint: $(LINT)
	$(LINT) run ./...

fmt:
	tofu fmt -recursive
	goimports -local github.com/abrosimov/gh-repo-settings -w cmd internal

fmt-check:
	tofu fmt -check -recursive

validate:
	for s in stacks/*; do tofu -chdir=$$s init -backend=false -input=false >/dev/null && tofu -chdir=$$s validate || exit 1; done

check: fmt-check vet test lint validate

init: build
	tofu -chdir=$(STACK) init -input=false

plan: init
	tofu -chdir=$(STACK) plan -input=false -out=tfplan

apply: build
	tofu -chdir=$(STACK) apply -input=false tfplan

key-init: build
	$(KEY_TOOL) init --recipients $(RECIPIENTS)

key-enroll: build
	$(KEY_TOOL) enroll --recipients $(RECIPIENTS)

key-list: build
	$(KEY_TOOL) list --recipients $(RECIPIENTS)
