LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest

CONTROLLER_TOOLS_VERSION ?= v0.23.0
ENVTEST_K8S_VERSION ?= 1.37.x

.PHONY: all
all: generate manifests build

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(CONTROLLER_GEN): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)

$(SETUP_ENVTEST): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate deepcopy code
	$(CONTROLLER_GEN) object paths="./api/..."

.PHONY: manifests
manifests: $(CONTROLLER_GEN) ## Generate CRDs and RBAC (also copied into the chart)
	$(CONTROLLER_GEN) crd rbac:roleName=devenv paths="./..." \
		output:crd:artifacts:config=config/crd \
		output:rbac:artifacts:config=config/rbac
	cp config/crd/*.yaml charts/devenv/crds/

.PHONY: fmt vet
fmt:
	go fmt ./...
vet:
	go vet ./...

.PHONY: test
test: generate manifests $(SETUP_ENVTEST) ## Run tests with envtest
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test ./... -count=1

.PHONY: web
web: ## Build the web console into web/dist
	cd web && pnpm install --frozen-lockfile && pnpm build

.PHONY: build
build: ## Build the binary (embeds web/dist if built)
	CGO_ENABLED=0 go build -o $(LOCALBIN)/devenv ./cmd/devenv

.PHONY: build-all
build-all: web build

.PHONY: run
run: manifests ## Run against the current kubeconfig (API on :8080, auth disabled)
	DEVENV_AUTH_MODE=none go run ./cmd/devenv --metrics-bind-address=0

.PHONY: chart-crds
chart-crds: manifests ## Copy generated CRDs into the Helm chart
	cp config/crd/*.yaml charts/devenv/crds/

.PHONY: chart-lint
chart-lint: chart-crds
	helm lint charts/devenv
	helm template devenv charts/devenv --namespace devenv-system >/dev/null

.PHONY: types
types: ## Generate TypeScript API types (web/src/gen) from Go
	GOBIN=$(LOCALBIN) go install github.com/gzuidhof/tygo@v0.2.21
	$(LOCALBIN)/tygo generate
	# tygo keeps the Go package qualifier on embedded types.
	sed -i 's/extends agent\./extends /' web/src/gen/api.ts
