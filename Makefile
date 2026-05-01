GO ?= go
KUSTOMIZE ?= kustomize
HELM ?= helm
DOCKER ?= docker
GOWORK ?= off
IMAGE ?= ghcr.io/appthrust/ocm-managed-serviceaccount-set-operator:dev
CHART_DIR := charts/ocm-managed-serviceaccount-set-operator
GOLANGCI_LINT ?= $(GO) tool -modfile tools/go.mod golangci-lint
CONTROLLER_GEN ?= $(GO) tool -modfile tools/go.mod controller-gen

.PHONY: test
test:
	GOWORK=$(GOWORK) $(GO) test ./...

.PHONY: fmt
fmt:
	gofmt -w api cmd

.PHONY: lint
lint:
	GOWORK=$(GOWORK) $(GOLANGCI_LINT) run ./...

.PHONY: generate
generate: generate-crds

.PHONY: generate-crds
generate-crds:
	GOWORK=$(GOWORK) $(CONTROLLER_GEN) object:headerFile=/dev/null crd paths=./api/... output:crd:dir=config/crd/bases
	mkdir -p $(CHART_DIR)/templates/crds
	cp config/crd/bases/authentication.appthrust.io_managedserviceaccountsets.yaml $(CHART_DIR)/templates/crds/authentication.appthrust.io_managedserviceaccountsets.yaml

.PHONY: verify-generated
verify-generated: generate
	git diff --exit-code -- api config/crd/bases $(CHART_DIR)/templates/crds

.PHONY: helm-lint
helm-lint:
	$(HELM) lint $(CHART_DIR)
	$(HELM) lint $(CHART_DIR) --values $(CHART_DIR)/values.test.yaml

.PHONY: manifests
manifests: generate-crds
	$(KUSTOMIZE) build config/default

.PHONY: helm-template
helm-template:
	@$(HELM) template ocm-managed-serviceaccount-set-operator $(CHART_DIR) \
		--namespace ocm-managed-serviceaccount-set-operator-system \
		--hide-notes

.PHONY: helm-template-test
helm-template-test:
	@$(HELM) template ocm-managed-serviceaccount-set-operator $(CHART_DIR) \
		--namespace ocm-managed-serviceaccount-set-operator-system \
		--values $(CHART_DIR)/values.test.yaml \
		--hide-notes

.PHONY: docker-build
docker-build:
	$(DOCKER) buildx build -t $(IMAGE) --load .

.PHONY: test-kest
test-kest:
	bun test test/kest

.PHONY: verify-static
verify-static:
	! rg -n 'sigs.k8s.io/cluster-api|RESTConfigFromKubeConfig|RESTConfigFromSecret|cluster-admin|Status\(\)\.Update\([^)]*(ClusterProfile|ManagedServiceAccount|ManifestWork|Placement)' api cmd internal config --glob '*.go' --glob '*.yaml'
	! rg -n 'kind:\s*ManagedServiceAccount\s*$$|kind:\s*ManifestWork\s*$$|- secrets|resources:\s*\["secrets"\]|verbs:\s*\["\*"\]|resources:\s*\["\*"\]' charts/ocm-managed-serviceaccount-set-operator/templates --glob '*.yaml' --glob '*.tpl'
