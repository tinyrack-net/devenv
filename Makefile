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
manifests: $(CONTROLLER_GEN) ## Generate CRDs and RBAC
	$(CONTROLLER_GEN) crd rbac:roleName=devenv paths="./..." \
		output:crd:artifacts:config=config/crd \
		output:rbac:artifacts:config=config/rbac

.PHONY: fmt vet
fmt:
	go fmt ./...
vet:
	go vet ./...

.PHONY: test
test: generate manifests $(SETUP_ENVTEST) ## Run tests with envtest
	KUBEBUILDER_ASSETS="$$($(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" \
		go test ./... -count=1

.PHONY: build
build:
	CGO_ENABLED=0 go build -o $(LOCALBIN)/devenv ./cmd/devenv

.PHONY: run
run: manifests
	go run ./cmd/devenv
