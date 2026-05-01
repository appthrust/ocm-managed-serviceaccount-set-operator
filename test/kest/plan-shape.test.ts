import { describe, expect } from "bun:test";
import { test } from "@appthrust/kest";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";

const chartPath = "charts/ocm-managed-serviceaccount-set-operator";

function helmTemplate(valuesFile?: string): string {
  const args = ["template", "omsa", chartPath, "--namespace", "omsa-system", "--hide-notes"];
  if (valuesFile !== undefined) {
    args.push("--values", valuesFile);
  }

  const result = spawnSync("helm", args, { encoding: "utf8" });
  if (result.status !== 0) {
    throw new Error(result.stderr + result.stdout);
  }

  return result.stdout;
}

function helmTemplateFailure(values: string): string {
  const tempDir = mkdtempSync(join(tmpdir(), "omsa-kest-"));
  try {
    const valuesFile = join(tempDir, "values.yaml");
    writeFileSync(valuesFile, values);
    const result = spawnSync(
      "helm",
      ["template", "omsa", chartPath, "--namespace", "omsa-system", "--hide-notes", "--values", valuesFile],
      { encoding: "utf8" },
    );
    expect(result.status).not.toBe(0);
    return result.stderr + result.stdout;
  } finally {
    rmSync(tempDir, { recursive: true, force: true });
  }
}

describe("plan and chart shape", () => {
  test("plan records the upstream API gap and required design sections", async () => {
    const plan = readFileSync("plan.md", "utf8");

    expect(plan).toContain("1777005621");
    expect(plan).toContain("Hard Constraints");
    expect(plan).toContain("Current Gaps / Migration Work");
    expect(plan).toContain("Target API");
    expect(plan).toContain("Review Log");
    expect(plan).toContain("PlacementDecision");
    expect(plan).toContain("ManagedServiceAccount");
    expect(plan).toContain("owner reference whose UID matches");
    expect(plan).toContain("permission profile");
  });

  test("chart installs only the operator, not per-cluster credentials", async () => {
    const manifest = helmTemplate();

    expect(manifest).toContain("kind: Deployment");
    expect(manifest).toContain("kind: ServiceAccount");
    expect(manifest).toContain("kind: ClusterRole");
    expect(manifest).not.toContain("apiVersion: authentication.open-cluster-management.io/v1beta1\nkind: ManagedServiceAccount");
    expect(manifest).not.toContain("apiVersion: work.open-cluster-management.io/v1\nkind: ManifestWork");
    expect(manifest).not.toContain("resources:\n      - secrets");
    expect(manifest).toContain("- namespaces\n    verbs:\n      - get\n      - list\n      - watch");
    expect(manifest).not.toContain("- namespaces\n    verbs:\n      - get\n      - list\n      - watch\n      - create");
  });

  test("chart keeps security defaults enabled", async () => {
    const manifest = helmTemplate(`${chartPath}/values.test.yaml`);
    const values = readFileSync(`${chartPath}/values.yaml`, "utf8");

    expect(manifest).toContain("readOnlyRootFilesystem: true");
    expect(manifest).toContain("allowPrivilegeEscalation: false");
    expect(manifest).toContain("runAsNonRoot: true");
    expect(manifest).toContain("resources:");
    expect(values).not.toContain("resources: {}");
  });

  test("chart CRD is synced with generated CRD", async () => {
    const crd = readFileSync("config/crd/bases/authentication.appthrust.io_managedserviceaccountsets.yaml", "utf8");

    expect(
      readFileSync(
        "charts/ocm-managed-serviceaccount-set-operator/templates/crds/authentication.appthrust.io_managedserviceaccountsets.yaml",
        "utf8",
      ),
    ).toBe(crd);
    expect(crd).toContain("aws-workload-identity-selfhosted-irsa");
    expect(crd).toContain("spec.remotePermissions.profileRefs is immutable");
    expect(crd).not.toContain("metadata.annotations");
  });

  test("chart validates unsafe values through values.schema.json", async () => {
    expect(
      helmTemplateFailure(`
image:
  pullPolicy: Sometimes
`),
    ).toContain("pullPolicy");

    expect(
      helmTemplateFailure(`
namespaceOverride: INVALID_NAMESPACE
`),
    ).toContain("namespaceOverride");

    expect(
      helmTemplateFailure(`
rbac:
  extraRules:
    - apiGroups: [""]
      resources: ["secrets"]
      verbs: ["get"]
`),
    ).toContain("extraRules");
  });

  test("test values exercise metrics, monitor, annotations, args, and env", async () => {
    const manifest = helmTemplate(`${chartPath}/values.test.yaml`);

    expect(manifest).toContain("kind: ServiceMonitor");
    expect(manifest).toContain("kind: Service");
    expect(manifest).toContain("appthrust.io/common: \"true\"");
    expect(manifest).toContain("appthrust.io/pod: \"true\"");
    expect(manifest).toContain("--zap-log-level=debug");
    expect(manifest).toContain("OMSA_TEST_MODE");
    expect(manifest).not.toContain("resources:\n      - secrets");
    expect(manifest).toContain("kind: CustomResourceDefinition");
  });
});
