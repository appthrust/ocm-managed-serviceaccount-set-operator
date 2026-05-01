#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PREFIX="${E2E_PREFIX:-msars-e2e}"
HUB_CLUSTER="${E2E_HUB_CLUSTER:-${PREFIX}-hub}"
SPOKE1_CLUSTER="${E2E_SPOKE1_CLUSTER:-${PREFIX}-spoke1}"
SPOKE2_CLUSTER="${E2E_SPOKE2_CLUSTER:-${PREFIX}-spoke2}"
IMAGE="${E2E_IMAGE:-localhost/ocm-managed-serviceaccount-replicaset-controller:e2e}"
CHART="${ROOT}/charts/ocm-managed-serviceaccount-replicaset-controller"
CONTROLLER_RELEASE="${E2E_CONTROLLER_RELEASE:-msars}"
CONTROLLER_DEPLOYMENT="${CONTROLLER_RELEASE}-ocm-managed-serviceaccount-replicaset-controller"
CLUSTERADM_REPO="${CLUSTERADM_REPO:-/srv/platform/refs/open-cluster-management-io/clusteradm}"
MSA_CHART="${MSA_CHART:-$(go env GOPATH)/pkg/mod/open-cluster-management.io/managed-serviceaccount@v0.10.0/charts/managed-serviceaccount}"
TMPDIR="${TMPDIR:-/tmp}"
WORK_DIR="$(mktemp -d "${TMPDIR%/}/msars-e2e.XXXXXX")"
export KUBECONFIG="${E2E_KUBECONFIG:-${WORK_DIR}/kubeconfig}"
KEEP_CLUSTER="${KEEP_E2E_CLUSTER:-false}"
PRESERVE_ON_FAILURE="${PRESERVE_E2E_ON_FAILURE:-true}"

log() {
  printf '\n[%s] %s\n' "$(date +%H:%M:%S)" "$*"
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "required command not found: $1" >&2
    exit 1
  }
}

cleanup() {
  local status=$?
  if [[ "${status}" -ne 0 ]]; then
    log "e2e failed; collecting hub diagnostics"
    kubectl --context "kind-${HUB_CLUSTER}" get pods -A || true
    kubectl --context "kind-${HUB_CLUSTER}" get managedclusters || true
    kubectl --context "kind-${HUB_CLUSTER}" get managedclusteraddons -A || true
    kubectl --context "kind-${HUB_CLUSTER}" get placementdecisions -A || true
    kubectl --context "kind-${HUB_CLUSTER}" get managedserviceaccountreplicasets.authentication.appthrust.io -A || true
    kubectl --context "kind-${HUB_CLUSTER}" logs -n ocm-managed-serviceaccount-replicaset-controller-system \
      "deploy/${CONTROLLER_DEPLOYMENT}" --tail=300 || true
    kubectl --context "kind-${HUB_CLUSTER}" logs -n open-cluster-management-addon \
      deploy/managed-serviceaccount-addon-manager --tail=300 || true
  fi

  if [[ "${KEEP_CLUSTER}" == "true" || ("${status}" -ne 0 && "${PRESERVE_ON_FAILURE}" == "true") ]]; then
    log "keeping kind clusters: ${HUB_CLUSTER}, ${SPOKE1_CLUSTER}, ${SPOKE2_CLUSTER}"
    log "kubeconfig: ${KUBECONFIG}"
  else
    log "deleting kind clusters"
    kind delete cluster --name "${SPOKE2_CLUSTER}" >/dev/null 2>&1 || true
    kind delete cluster --name "${SPOKE1_CLUSTER}" >/dev/null 2>&1 || true
    kind delete cluster --name "${HUB_CLUSTER}" >/dev/null 2>&1 || true
    rm -rf "${WORK_DIR}"
  fi
  exit "${status}"
}
trap cleanup EXIT

wait_for_rollout() {
  local context=$1
  local namespace=$2
  local deployment=$3
  kubectl --context "${context}" rollout status "deploy/${deployment}" -n "${namespace}" --timeout=300s
}

wait_for_condition() {
  local context=$1
  local resource=$2
  local jsonpath=$3
  local want=$4
  local timeout=${5:-300}
  local namespace=${6:-}
  local start
  start=$(date +%s)
  while true; do
    local got
    if [[ -n "${namespace}" ]]; then
      got=$(kubectl --context "${context}" get "${resource}" -n "${namespace}" -o "jsonpath=${jsonpath}" 2>/dev/null || true)
    else
      got=$(kubectl --context "${context}" get "${resource}" -o "jsonpath=${jsonpath}" 2>/dev/null || true)
    fi
    if [[ "${got}" == "${want}" ]]; then
      return 0
    fi
    if (( $(date +%s) - start > timeout )); then
      echo "timed out waiting for ${resource} ${jsonpath}: got ${got@Q}, want ${want@Q}" >&2
      if [[ -n "${namespace}" ]]; then
        kubectl --context "${context}" get "${resource}" -n "${namespace}" -o yaml || true
      else
        kubectl --context "${context}" get "${resource}" -o yaml || true
      fi
      return 1
    fi
    sleep 5
  done
}

extract_join_flag() {
  local command_line=$1
  local flag=$2
  local previous=""
  local word
  for word in ${command_line}; do
    if [[ "${previous}" == "${flag}" ]]; then
      printf '%s\n' "${word}"
      return 0
    fi
    case "${word}" in
      "${flag}="*)
        printf '%s\n' "${word#*=}"
        return 0
        ;;
    esac
    previous="${word}"
  done
  return 1
}

require_cmd kind
require_cmd kubectl
require_cmd helm
require_cmd docker
require_cmd go
require_cmd bun

log "building clusteradm from ${CLUSTERADM_REPO}"
mkdir -p "${WORK_DIR}/bin"
(cd "${CLUSTERADM_REPO}" && GOWORK=off go build -mod=vendor -o "${WORK_DIR}/bin/clusteradm" ./cmd/clusteradm/clusteradm.go)
export PATH="${WORK_DIR}/bin:${PATH}"

log "resetting kind clusters"
kind delete cluster --name "${SPOKE2_CLUSTER}" >/dev/null 2>&1 || true
kind delete cluster --name "${SPOKE1_CLUSTER}" >/dev/null 2>&1 || true
kind delete cluster --name "${HUB_CLUSTER}" >/dev/null 2>&1 || true

log "creating kind hub and two spokes"
kind create cluster --name "${HUB_CLUSTER}" --wait 120s
kind create cluster --name "${SPOKE1_CLUSTER}" --wait 120s
kind create cluster --name "${SPOKE2_CLUSTER}" --wait 120s

log "building and loading controller image ${IMAGE}"
docker build -t "${IMAGE}" "${ROOT}"
kind load docker-image --name "${HUB_CLUSTER}" "${IMAGE}"

log "installing OCM hub with ClusterProfile feature enabled"
kubectl config use-context "kind-${HUB_CLUSTER}" >/dev/null
JOIN_FILE="${WORK_DIR}/join-command.txt"
clusteradm init --wait \
  --feature-gates=ClusterProfile=true \
  --output-join-command-file "${JOIN_FILE}"

JOIN_COMMAND="$(tr '\n' ' ' < "${JOIN_FILE}")"
HUB_TOKEN="$(extract_join_flag "${JOIN_COMMAND}" "--hub-token")"
HUB_APISERVER="$(extract_join_flag "${JOIN_COMMAND}" "--hub-apiserver")"
if [[ -z "${HUB_TOKEN}" || -z "${HUB_APISERVER}" ]]; then
  echo "failed to parse clusteradm join command: ${JOIN_COMMAND}" >&2
  exit 1
fi

log "joining spokes to OCM hub"
for spoke in "${SPOKE1_CLUSTER}" "${SPOKE2_CLUSTER}"; do
  kubectl config use-context "kind-${spoke}" >/dev/null
  clusteradm join \
    --hub-token "${HUB_TOKEN}" \
    --hub-apiserver "${HUB_APISERVER}" \
    --cluster-name "${spoke}" \
    --force-internal-endpoint-lookup
done

log "accepting both spokes"
kubectl config use-context "kind-${HUB_CLUSTER}" >/dev/null
clusteradm accept --clusters "${SPOKE1_CLUSTER},${SPOKE2_CLUSTER}" --wait
for spoke in "${SPOKE1_CLUSTER}" "${SPOKE2_CLUSTER}"; do
  wait_for_condition "kind-${HUB_CLUSTER}" "managedcluster/${spoke}" \
    '{.status.conditions[?(@.type=="ManagedClusterConditionAvailable")].status}' "True" 600
done

log "installing managed-serviceaccount addon"
helm --kube-context "kind-${HUB_CLUSTER}" upgrade --install managed-serviceaccount "${MSA_CHART}" \
  --namespace open-cluster-management-addon \
  --create-namespace \
  --set agentInstallAll=true \
  --set featureGates.clusterProfileCredSyncer=true
wait_for_rollout "kind-${HUB_CLUSTER}" open-cluster-management-addon managed-serviceaccount-addon-manager
for spoke in "${SPOKE1_CLUSTER}" "${SPOKE2_CLUSTER}"; do
  wait_for_condition "kind-${HUB_CLUSTER}" "managedclusteraddon/managed-serviceaccount" \
    '{.status.conditions[?(@.type=="Available")].status}' "True" 600 "${spoke}"
done

log "installing controller on hub"
helm --kube-context "kind-${HUB_CLUSTER}" upgrade --install "${CONTROLLER_RELEASE}" "${CHART}" \
  --namespace ocm-managed-serviceaccount-replicaset-controller-system \
  --create-namespace \
  --set image.registry=localhost \
  --set image.repository=ocm-managed-serviceaccount-replicaset-controller \
  --set image.tag=e2e \
  --set image.pullPolicy=IfNotPresent \
  --set controller.leaderElection.enabled=false \
  --set controller.clusterProfileProvider.enabled=true
wait_for_rollout "kind-${HUB_CLUSTER}" ocm-managed-serviceaccount-replicaset-controller-system \
  "${CONTROLLER_DEPLOYMENT}"

log "running Kest e2e against hub + two spokes"
E2E_HUB_CLUSTER="${HUB_CLUSTER}" \
E2E_SPOKE1_CLUSTER="${SPOKE1_CLUSTER}" \
E2E_SPOKE2_CLUSTER="${SPOKE2_CLUSTER}" \
KEST_SHOW_REPORT="${KEST_SHOW_REPORT:-0}" \
bun test "${ROOT}/test/kest/e2e-ocm.test.ts" --timeout 900000

log "e2e completed"
