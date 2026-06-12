PROJECT     ?= karpenter-operator
ORG_PATH    ?= github.com/openshift
REPO_PATH   ?= $(ORG_PATH)/$(PROJECT)
VERSION     ?= $(shell git describe --always --dirty --abbrev=7)
LD_FLAGS    ?= -X $(REPO_PATH)/pkg/version.Raw=$(VERSION)
BUILD_DEST  ?= bin/karpenter-operator

# Image configuration
IMAGE_TAG_BASE ?= quay.io/openshift/karpenter-operator
IMG            ?= $(IMAGE_TAG_BASE):$(VERSION)
OPERAND_IMG    ?= quay.io/openshift/origin-aws-karpenter-provider-aws:latest

GOFLAGS ?= -mod=vendor
export GOFLAGS
GOPROXY ?=
export GOPROXY

# CONTAINER_TOOL defines the container tool to be used for building images.
ifeq ($(shell command -v podman > /dev/null 2>&1 ; echo $$?), 0)
CONTAINER_TOOL ?= podman
else
CONTAINER_TOOL ?= docker
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
YQ ?= $(LOCALBIN)/yq
GOLANGCI_LINT ?= $(LOCALBIN)/golangci-lint
HELM ?= $(LOCALBIN)/helm

## Tool Versions
YQ_VERSION            ?= v4.53.2
GOLANGCI_LINT_VERSION ?= v2.12.1
HELM_VERSION          ?= v4.2.0

##@ Tools

.PHONY: yq
yq: $(YQ) ## Install yq locally if necessary.
$(YQ): $(LOCALBIN)
	@if test -s $(YQ) && $(YQ) --version 2>/dev/null | grep -q "$(YQ_VERSION)"; then \
	  true; \
	else \
	  echo "Installing yq $(YQ_VERSION)..."; \
	  GOBIN=$(LOCALBIN) GOFLAGS= go install github.com/mikefarah/yq/v4@$(YQ_VERSION); \
	fi

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Install golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	@if test -s $(GOLANGCI_LINT) && $(GOLANGCI_LINT) version 2>/dev/null | grep -q "$(subst v,,$(GOLANGCI_LINT_VERSION))"; then \
	  true; \
	else \
	  echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."; \
	  GOBIN=$(LOCALBIN) GOFLAGS= go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION); \
	fi

.PHONY: helm
helm: $(HELM) ## Install helm locally if necessary.
$(HELM): $(LOCALBIN)
	@PLATFORM=$$(uname -s | tr '[:upper:]' '[:lower:]')-$$(uname -m | sed 's/x86_64/amd64/' | sed 's/aarch64/arm64/'); \
	if test -s $(HELM) && $(HELM) version 2>/dev/null | grep -q "$(HELM_VERSION)"; then \
	  true; \
	else \
	  echo "Installing helm $(HELM_VERSION)..."; \
	  curl -sSL "https://get.helm.sh/helm-$(HELM_VERSION)-$${PLATFORM}.tar.gz" \
	    | tar -xz --strip-components=1 -C $(LOCALBIN) "$${PLATFORM}/helm"; \
	fi

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: lint
lint: $(GOLANGCI_LINT) ## Run golangci-lint against code.
	GOFLAGS= $(GOLANGCI_LINT) run ./...

.PHONY: manifest-diff
manifest-diff: $(YQ) $(HELM) ## Check RBAC manifests are in sync (no writes).
	YQ=$(YQ) HELM=$(HELM) hack/manifest-diff-upstream.sh
	YQ=$(YQ) hack/manifest-diff.sh

.PHONY: manifest-diff-sync
manifest-diff-sync: $(YQ) $(HELM) ## Regenerate RBAC manifests from sources.
	YQ=$(YQ) HELM=$(HELM) hack/manifest-diff-upstream.sh --sync
	YQ=$(YQ) hack/manifest-diff.sh --sync

.PHONY: verify-deps
verify-deps: ## Verify go.mod and vendor are tidy.
	go mod tidy
	go mod vendor
	git diff --exit-code go.mod go.sum vendor/

.PHONY: test
test: ## Run unit tests.
	go test ./pkg/... -count=1

.PHONY: verify
verify: vet fmt lint manifest-diff verify-deps test ## Run all verification checks.

.PHONY: vendor
vendor: ## Tidy and vendor Go modules.
	go mod tidy
	go mod vendor

##@ Build

.PHONY: build
build: ## Build the operator binary.
	go build -ldflags "$(LD_FLAGS)" -o "$(BUILD_DEST)" "$(REPO_PATH)/cmd"

.PHONY: run
run: fmt vet ## Run the operator from your host.
	go run ./cmd

.PHONY: docker-build
docker-build: ## Build docker image with the operator.
	$(CONTAINER_TOOL) build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push docker image with the operator.
	$(CONTAINER_TOOL) push $(IMG)

##@ Deployment

# Dev deploy configuration — override on the command line:
#   make deploy IMG=quay.io/you/karpenter-operator:dev OPERAND_IMG=quay.io/you/karpenter:dev CLUSTER_NAME=my-cluster
#   make deploy DEV=true  — also sets imagePullPolicy: Always for rapid iteration with :latest tags
CLUSTER_NAME ?=
# TODO(maxcao13): remove DEV flag before GA — imagePullPolicy should not be Always in production
DEV ?=

.PHONY: predeploy
predeploy: ## Copy and patch install manifests into _output/ for dev deployment.
	@rm -rf _output
	@mkdir -p _output
	@cp install/*.yaml _output/
	@sed -i 's|image: quay.io/openshift/origin-karpenter-operator:.*|image: $(IMG)|' _output/05_deployment.yaml
	@sed -i 's|value: quay.io/openshift/origin-aws-karpenter-provider-aws:.*|value: $(OPERAND_IMG)|' _output/05_deployment.yaml
	@sed -i '/name: CLUSTER_NAME/{n;s|value: ".*"|value: "$(CLUSTER_NAME)"|}' _output/05_deployment.yaml
	@if [ "$(DEV)" = "true" ]; then \
		sed -i '/- name: karpenter-operator$$/a\        imagePullPolicy: Always' _output/05_deployment.yaml; \
		sed -i '/name: KARPENTER_IMAGE_AWS/i\        - name: DEV_IMAGE_PULL_POLICY\n          value: "Always"' _output/05_deployment.yaml; \
	fi

.PHONY: apply
apply: ## Apply manifests from _output/ to the cluster.
	kubectl apply --server-side --force-conflicts -f _output/00_namespace.yaml
	kubectl apply --server-side --force-conflicts -f _output/

.PHONY: deploy
deploy: predeploy apply ## Deploy operator to the K8s cluster.

.PHONY: undeploy
undeploy: ## Remove operator from the K8s cluster.
	@[ -d _output ] && kubectl delete --ignore-not-found -f _output/ || true

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
