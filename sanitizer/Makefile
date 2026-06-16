# Image URL to use all building/pushing image targets
IMG ?= sanitizer:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: goimports ## Run goimports against code.
	$(GOIMPORTS) -w .

.PHONY: vet
vet: ## Run go vet against code.

.PHONY: test
test: kernel fmt vet ## Run tests.
	KERNEL_ASSETS="$(shell $(KERNEL) use $(KERNEL_KERNEL_VERSION) --dir $(LOCALBIN) -p path)" \
	SANITIZER_KERNEL_MODULES="failover,net_failover,virtio_net,virtio_blk" \
		go test ./e2e -coverprofile cover.out

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

.PHONY: add-license
add-license: addlicense ## Add license headers to all go files.
	find . -name '*.go' -exec $(ADDLICENSE) -f hack/license-header.txt {} +

.PHONY: check-license
check-license: addlicense ## Check that every file has a license header present.
	find . -name '*.go' -exec $(ADDLICENSE) -check -c 'IronCore authors' {} +

.PHONY: check
check: add-license fmt lint test # Generate manifests, code, lint, add licenses, test

##@ Build

.PHONY: build
build: kernel u-root fmt vet ## Build manager binary.
	U_ROOT=$(U_ROOT) KERNEL=$(KERNEL) ./hack/build.sh \
		-k $(KERNEL_KERNEL_VERSION) \
		-m failover -m net_failover -m virtio_net -m virtio_blk \
		-o bin/initramfs.cpio

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

## Tool Binaries
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
GOIMPORTS ?= $(LOCALBIN)/goimports
ADDLICENSE ?= $(LOCALBIN)/addlicense
U_ROOT ?= $(LOCALBIN)/u-root
KERNEL ?= $(LOCALBIN)/kernel

## Tool Versions
GOLANGCI_LINT_VERSION ?= v2.12
GOIMPORTS_VERSION ?= v0.45.0
ADDLICENSE_VERSION ?= v1.1.1
U_ROOT_VERSION ?= v0.16.0
# KERNEL_KERNEL_VERSION is the version of the kernel for the kernel tool to use.
KERNEL_KERNEL_VERSION ?= 6.12.90-2-cloud

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))
	@test -f .custom-gcl.yml && { \
		echo "Building custom golangci-lint with plugins..." && \
		$(GOLANGCI_LINT) custom --destination $(LOCALBIN) --name golangci-lint-custom && \
		mv -f $(LOCALBIN)/golangci-lint-custom $(GOLANGCI_LINT); \
	} || true

.PHONY: goimports
goimports: $(GOIMPORTS) ## Download goimports locally if necessary.
$(GOIMPORTS): $(LOCALBIN)
	$(call go-install-tool,$(GOIMPORTS),golang.org/x/tools/cmd/goimports,$(GOIMPORTS_VERSION))

.PHONY: addlicense
addlicense: $(ADDLICENSE) ## Download addlicense locally if necessary.
$(ADDLICENSE): $(LOCALBIN)
	$(call go-install-tool,$(ADDLICENSE),github.com/google/addlicense,$(ADDLICENSE_VERSION))

.PHONY: u-root
u-root: $(U_ROOT) ## Download u-root locally if necessary.
$(U_ROOT): $(LOCALBIN)
	$(call go-install-tool,$(U_ROOT),github.com/u-root/u-root,$(U_ROOT_VERSION))

.PHONY: kernel
kernel: $(KERNEL)
$(KERNEL): $(LOCALBIN) ## Install the kernel tool if necessary.
	cd hack/kernel && GOBIN=$(LOCALBIN) go install .

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# Note: All paths are quoted to work in directories containing spaces or parentheses.
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" || true ;\
GOBIN="$(LOCALBIN)" go install "$${package}" ;\
mv "$(1)" "$(1)-$(3)" ;\
} ;\
ln -sf "$(1)-$(3)" "$(1)"
endef
