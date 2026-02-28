# Image URL to use all building/pushing image targets
IMG ?= controller:latest

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
HELM ?= helm
CHART_DIR ?= charts/redis-operator
HELM_PACKAGE_DIR ?= .chart-packages
HELM_TEMPLATE_KUBE_VERSION ?= 1.35.0

E2E_KIND_CLUSTER ?= redis-operator-e2e
E2E_NAMESPACE ?= redis-e2e
E2E_IMG ?= controller:e2e
E2E_HELM_RELEASE ?= redis-operator
E2E_OPERATOR_NAMESPACE ?= redis-operator-system
E2E_OPERATOR_DEPLOYMENT ?= redis-operator-controller-manager
E2E_CHAINSAW_DIR ?= test/e2e/chainsaw
E2E_CHAINSAW_CONFIG ?= $(E2E_CHAINSAW_DIR)/.chainsaw.yaml
E2E_CHAINSAW_SUITES ?= cluster,failover
E2E_CHAINSAW_SKIP_DELETE ?= true
E2E_ARTIFACT_DIR_LOCAL ?= test/e2e/artifacts/local
E2E_ARTIFACT_DIR_PR ?= test/e2e/artifacts/pr
E2E_CLUSTER_DOMAIN ?= cluster.local
E2E_DNS_PREFLIGHT ?= true

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

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet ## Run unit tests.
	go test ./... -coverprofile cover.out

.PHONY: test-integration
test-integration: manifests generate fmt vet setup-envtest ## Run envtest integration tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -tags=integration ./internal/controller -v -count=1

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: golangci-lint ## Verify golangci-lint linter configuration
	$(GOLANGCI_LINT) config verify

.PHONY: helm-lint
helm-lint: ## Run helm lint against the redis-operator chart.
	$(HELM) lint $(CHART_DIR)

.PHONY: helm-template
helm-template: ## Render redis-operator chart templates.
	$(HELM) template redis-operator $(CHART_DIR) --namespace $(E2E_OPERATOR_NAMESPACE) --include-crds --kube-version $(HELM_TEMPLATE_KUBE_VERSION) >/dev/null

.PHONY: helm-test
helm-test: helm-lint helm-template ## Run helm chart static checks.

.PHONY: helm-package
helm-package: ## Package redis-operator chart.
	mkdir -p $(HELM_PACKAGE_DIR)
	$(HELM) package $(CHART_DIR) --destination $(HELM_PACKAGE_DIR)

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name redis-operator-builder
	$(CONTAINER_TOOL) buildx use redis-operator-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm redis-operator-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default > dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ E2E

.PHONY: e2e-check-tools
e2e-check-tools: ## Verify required local tools for e2e workflows.
	@for bin in $(KUBECTL) $(KIND) $(CHAINSAW) $(CONTAINER_TOOL) openssl; do \
		if ! command -v $$bin >/dev/null 2>&1; then \
			echo "Error: $$bin is required but not found in PATH"; \
			exit 1; \
		fi; \
	done

.PHONY: e2e-check-tools-pr
e2e-check-tools-pr: e2e-check-tools ## Verify extra tools required for image-based PR e2e flow.
	@if ! command -v $(HELM) >/dev/null 2>&1; then \
		echo "Error: $(HELM) is required for PR e2e flow"; \
		exit 1; \
	fi

.PHONY: e2e-check-tools-local
e2e-check-tools-local: ## Verify extra tools required for local e2e flow.
	@if ! command -v ktctl >/dev/null 2>&1; then \
		echo "Error: ktctl is required for local e2e flow"; \
		exit 1; \
	fi

.PHONY: e2e-kind-up
e2e-kind-up: e2e-check-tools ## Create or reuse kind cluster used by e2e.
	@if ! $(KIND) get clusters | grep -qx "$(E2E_KIND_CLUSTER)"; then \
		$(KIND) create cluster --name "$(E2E_KIND_CLUSTER)" --config test/e2e/kind-config.yaml --wait 120s; \
	fi
	$(KUBECTL) config use-context kind-$(E2E_KIND_CLUSTER)

.PHONY: e2e-reset-namespace
e2e-reset-namespace: ## Recreate the e2e namespace.
	$(KUBECTL) delete namespace $(E2E_NAMESPACE) --ignore-not-found=true
	$(KUBECTL) create namespace $(E2E_NAMESPACE)

.PHONY: e2e-local-up
e2e-local-up: e2e-kind-up e2e-check-tools-local ## Prepare cluster prerequisites for local ktctl e2e runs.
	make install
	$(MAKE) e2e-reset-namespace
	$(KUBECTL) -n redis-operator-system scale deployment redis-operator-controller-manager --replicas=0 >/dev/null 2>&1 || true

.PHONY: e2e-local
e2e-local: e2e-local-up ## Run e2e with local controller via ktctl tunnel.
	E2E_KIND_CLUSTER=$(E2E_KIND_CLUSTER) \
	E2E_NAMESPACE=$(E2E_NAMESPACE) \
	E2E_ARTIFACT_DIR_LOCAL=$(E2E_ARTIFACT_DIR_LOCAL) \
	E2E_CHAINSAW_DIR=$(E2E_CHAINSAW_DIR) \
	E2E_CHAINSAW_CONFIG=$(E2E_CHAINSAW_CONFIG) \
	E2E_CHAINSAW_SKIP_DELETE=$(E2E_CHAINSAW_SKIP_DELETE) \
	E2E_CLUSTER_DOMAIN=$(E2E_CLUSTER_DOMAIN) \
	E2E_DNS_PREFLIGHT=$(E2E_DNS_PREFLIGHT) \
	./hack/e2e/run-local.sh

.PHONY: e2e-local-dump
e2e-local-dump: ## Collect diagnostics for local e2e runs.
	./hack/e2e/dump.sh $(E2E_ARTIFACT_DIR_LOCAL) $(E2E_NAMESPACE)

.PHONY: e2e-local-down
e2e-local-down: ## Stop residual local e2e controller process.
	@if [ -f "$(E2E_ARTIFACT_DIR_LOCAL)/controller.pid" ]; then \
		kill "$$(cat "$(E2E_ARTIFACT_DIR_LOCAL)/controller.pid")" >/dev/null 2>&1 || true; \
	fi

.PHONY: e2e
e2e: e2e-kind-up e2e-check-tools-pr ## Run e2e path with kind + built image + helm install/upgrade.
	E2E_KIND_CLUSTER=$(E2E_KIND_CLUSTER) \
	E2E_NAMESPACE=$(E2E_NAMESPACE) \
	E2E_HELM_RELEASE=$(E2E_HELM_RELEASE) \
	E2E_OPERATOR_NAMESPACE=$(E2E_OPERATOR_NAMESPACE) \
	E2E_OPERATOR_DEPLOYMENT=$(E2E_OPERATOR_DEPLOYMENT) \
	E2E_ARTIFACT_DIR_PR=$(E2E_ARTIFACT_DIR_PR) \
	E2E_CHAINSAW_DIR=$(E2E_CHAINSAW_DIR) \
	E2E_CHAINSAW_CONFIG=$(E2E_CHAINSAW_CONFIG) \
	E2E_CHAINSAW_SUITES=$(E2E_CHAINSAW_SUITES) \
	E2E_IMG=$(E2E_IMG) \
	KUBECTL_BIN=$(KUBECTL) \
	HELM_BIN=$(HELM) \
	CONTAINER_TOOL_BIN=$(CONTAINER_TOOL) \
	./hack/e2e/run-pr.sh

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
ENVTEST ?= $(LOCALBIN)/setup-envtest
KIND ?= kind
CHAINSAW ?= chainsaw
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint

## Tool Versions
KUSTOMIZE_VERSION ?= v5.6.0
CONTROLLER_TOOLS_VERSION ?= v0.18.0
#ENVTEST_VERSION is the version of controller-runtime release branch to fetch the envtest setup script (i.e. release-0.20)
ENVTEST_VERSION ?= $(shell go list -m -f "{{ .Version }}" sigs.k8s.io/controller-runtime | awk -F'[v.]' '{printf "release-%d.%d", $$2, $$3}')
#ENVTEST_K8S_VERSION is the version of Kubernetes to use for setting up ENVTEST binaries (i.e. 1.31)
ENVTEST_K8S_VERSION ?= $(shell go list -m -f "{{ .Version }}" k8s.io/api | awk -F'[v.]' '{printf "1.%d", $$3}')
GOLANGCI_LINT_VERSION ?= v2.3.0

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: setup-envtest
setup-envtest: envtest ## Download the binaries required for ENVTEST in the local bin directory.
	@echo "Setting up envtest binaries for Kubernetes version $(ENVTEST_K8S_VERSION)..."
	@$(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path || { \
		echo "Error: Failed to set up envtest binaries for version $(ENVTEST_K8S_VERSION)."; \
		exit 1; \
	}

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,$(GOLANGCI_LINT_VERSION))

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f $(1) ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
mv $(1) $(1)-$(3) ;\
} ;\
ln -sf $$(realpath $(1)-$(3)) $(1)
endef
