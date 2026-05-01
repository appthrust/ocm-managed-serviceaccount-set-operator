#!/usr/bin/env bash
set -euo pipefail

chart="charts/ocm-managed-serviceaccount-set-operator/Chart.yaml"
version="$(awk '/^version:/ {print $2}' "${chart}")"
sed -Ei "s/^appVersion: .*/appVersion: \"${version}\"/" "${chart}"
sed -Ei "s/^  tag: .*/  tag: \"${version}\"/" charts/ocm-managed-serviceaccount-set-operator/values.yaml

