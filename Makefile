GO ?= go
KUSTOMIZE ?= kustomize
HELM ?= helm
DOCKER ?= docker
GOWORK ?= off
IMAGE ?= ghcr.io/appthrust/ocm-managed-serviceaccount-replicaset-controller:dev
CHART_DIR := charts/ocm-managed-serviceaccount-replicaset-controller
GOLANGCI_LINT ?= $(GO) tool -modfile tools/go.mod golangci-lint
CONTROLLER_GEN ?= $(GO) tool -modfile tools/go.mod controller-gen

.PHONY: test
test:
	GOWORK=$(GOWORK) $(GO) test ./...

.PHONY: fmt
fmt:
	gofmt -w api cmd internal

.PHONY: lint
lint:
	GOWORK=$(GOWORK) $(GOLANGCI_LINT) run ./...

.PHONY: generate
generate: generate-crds

.PHONY: generate-crds
generate-crds:
	GOWORK=$(GOWORK) $(CONTROLLER_GEN) object:headerFile=/dev/null crd paths=./api/... output:crd:dir=config/crd/bases
	mkdir -p $(CHART_DIR)/templates/crds
	cp config/crd/bases/authentication.appthrust.io_managedserviceaccountreplicasets.yaml $(CHART_DIR)/templates/crds/authentication.appthrust.io_managedserviceaccountreplicasets.yaml

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
	@$(HELM) template ocm-managed-serviceaccount-replicaset-controller $(CHART_DIR) \
		--namespace ocm-managed-serviceaccount-replicaset-controller-system \
		--hide-notes

.PHONY: helm-template-test
helm-template-test:
	@$(HELM) template ocm-managed-serviceaccount-replicaset-controller $(CHART_DIR) \
		--namespace ocm-managed-serviceaccount-replicaset-controller-system \
		--values $(CHART_DIR)/values.test.yaml \
		--hide-notes

.PHONY: docker-build
docker-build:
	$(DOCKER) buildx build -t $(IMAGE) --load .

.PHONY: test-chart
test-chart:
	bun test test/kest/plan-shape.test.ts

.PHONY: test-e2e
test-e2e:
	hack/e2e-kind.sh

.PHONY: verify-static
verify-static:
	! rg -n 'sigs.k8s.io/cluster-api|RESTConfigFromKubeConfig|RESTConfigFromSecret|cluster-admin|Status\(\)\.Update\([^)]*(ClusterProfile|ManagedServiceAccount|ManifestWork|Placement)' api cmd internal config --glob '*.go' --glob '*.yaml'
	! $(MAKE) --no-print-directory helm-template | rg -n 'kind:\s*ManagedServiceAccount\s*$$|kind:\s*ManifestWork\s*$$|- secrets|resources:\s*\["secrets"\]|verbs:\s*\["\*"\]|resources:\s*\["\*"\]'
	! rg -n -- 'managedclusters|managedclustersets|clusterprofiles/status|managedserviceaccounts/status|manifestworks/status' config/rbac charts/ocm-managed-serviceaccount-replicaset-controller/templates/rbac.yaml
	! rg -n --glob '!leader_election*' --glob '!rbac-leader-election*' -- '- update' config/rbac charts/ocm-managed-serviceaccount-replicaset-controller/templates/rbac.yaml
	! rg -n -U 'resources:\n\s+- namespaces\n\s+verbs:\n\s+- get\n\s+- list' config/rbac charts/ocm-managed-serviceaccount-replicaset-controller/templates/rbac.yaml
	! rg -n -U 'resources:\n\s+- secrets' config/rbac
