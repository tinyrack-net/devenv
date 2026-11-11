LOCALBIN ?= $(shell pwd)/bin
SETUP_ENVTEST ?= $(LOCALBIN)/setup-envtest

ENVTEST_K8S_VERSION ?= 1.37.x

.PHONY: all
all: build

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

$(SETUP_ENVTEST): | $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest

.PHONY: fmt vet
fmt:
	go fmt ./...
vet:
	go vet ./...

.PHONY: test
test: $(SETUP_ENVTEST) ## Run tests with envtest (DEVENV_TEST_POSTGRES=postgres://... adds PostgreSQL)
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
run: ## Run against the current kubeconfig (API on :8080, auth disabled, SQLite in ./bin)
	go run ./cmd/devenv --metrics-bind-address=0 --config=hack/dev.yaml

.PHONY: chart-lint
chart-lint:
	helm lint charts/devenv
	helm template devenv charts/devenv --namespace devenv-system >/dev/null

.PHONY: types
types: ## Generate TypeScript API types (web/src/gen) from Go
	GOBIN=$(LOCALBIN) go install github.com/gzuidhof/tygo@v0.2.21
	$(LOCALBIN)/tygo generate
	# tygo keeps the Go package qualifier on embedded types.
	sed -i 's/extends agent\./extends /' web/src/gen/api.ts
