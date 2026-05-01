import { describe, expect } from "bun:test";
import { test } from "@appthrust/kest";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";

const chartPath = "charts/ocm-managed-serviceaccount-replicaset-controller";

function helmTemplate(valuesFile?: string): string {
  const args = ["template", "msars", chartPath, "--namespace", "msars-system", "--hide-notes"];
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
  const tempDir = mkdtempSync(join(tmpdir(), "msars-kest-"));
  try {
    const valuesFile = join(tempDir, "values.yaml");
    writeFileSync(valuesFile, values);
    const result = spawnSync(
      "helm",
      ["template", "msars", chartPath, "--namespace", "msars-system", "--hide-notes", "--values", valuesFile],
      { encoding: "utf8" },
    );
    expect(result.status).not.toBe(0);
    return result.stderr + result.stdout;
  } finally {
    rmSync(tempDir, { recursive: true, force: true });
  }
}

describe("chart and API shape", () => {
  test("chart installs only the controller, not per-cluster credentials", async () => {
    const manifest = helmTemplate();

    expect(manifest).toContain("kind: Deployment");
    expect(manifest).toContain("kind: ServiceAccount");
    expect(manifest).toContain("kind: ClusterRole");
    expect(manifest).not.toContain("apiVersion: authentication.open-cluster-management.io/v1beta1\nkind: ManagedServiceAccount");
    expect(manifest).not.toContain("apiVersion: work.open-cluster-management.io/v1\nkind: ManifestWork");
    expect(manifest).not.toContain("resources:\n      - secrets");
    expect(manifest).not.toContain("- managedclusters");
    expect(manifest).not.toContain("- managedclustersets");
    // `- update` is permitted only on the leader-election Lease.
    // Confirm it appears exactly once, scoped to the `leases` resource.
    expect(manifest).toContain("- leases\n    verbs:\n      - get\n      - create\n      - update");
    expect(manifest.split("- update").length - 1).toBe(1);
    expect(manifest).toContain("- namespaces\n    verbs:\n      - get");
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

  test("chart gates ClusterProfile provider credentials behind an explicit value", async () => {
    const tempDir = mkdtempSync(join(tmpdir(), "msars-provider-"));
    try {
      const valuesFile = join(tempDir, "values.yaml");
      writeFileSync(
        valuesFile,
        `
controller:
  clusterProfileProvider:
    enabled: true
    providerFilePath: /etc/msars/clusterprofile-provider-file.json
    providerName: open-cluster-management
    credentialsCommand: /cp-creds
`,
      );
      const manifest = helmTemplate(valuesFile);

      expect(manifest).toContain("--clusterprofile-provider-file=/etc/msars/clusterprofile-provider-file.json");
      expect(manifest).toContain("kind: ConfigMap");
      expect(manifest).toContain('"command": "/cp-creds"');
      expect(manifest).toContain("resources:\n      - secrets\n    verbs:\n      - get");
    } finally {
      rmSync(tempDir, { recursive: true, force: true });
    }
  });

  test("chart CRD is synced with generated CRD", async () => {
    const crd = readFileSync("config/crd/bases/authentication.appthrust.io_managedserviceaccountreplicasets.yaml", "utf8");

    expect(
      readFileSync(
        "charts/ocm-managed-serviceaccount-replicaset-controller/templates/crds/authentication.appthrust.io_managedserviceaccountreplicasets.yaml",
        "utf8",
      ),
    ).toBe(crd);
    expect(crd).toContain("ManagedServiceAccountReplicaSet");
    expect(crd).toContain("managedserviceaccountreplicasets");
    expect(crd).toContain("maxItems: 16");
    expect(crd).toContain("maxProperties: 32");
    expect(crd).toContain("metadata:");
    expect(crd).toContain("grants:");
    expect(crd).toContain("forEachNamespace:");
    expect(crd).toContain("matchExpressions:");
    expect(crd).toContain("spec.rbac.grants[].id must be unique");
    expect(crd).toContain("spec.rbac.grants[].metadata.labels must not contain controller-reserved");
    expect(crd).not.toContain("clusterRoles:");
    expect(crd).not.toContain("roles:");
    expect(crd).not.toContain("remotePermissions");
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
    expect(manifest).toContain("scheme: https");
    expect(manifest).toContain('bearerTokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token"');
    expect(manifest).toContain("insecureSkipVerify: true");
  });

  test("chart rejects serviceMonitor.scheme=https without tlsConfig", async () => {
    expect(
      helmTemplateFailure(`
serviceMonitor:
  enabled: true
  scheme: https
  tlsConfig: null
`),
    ).toContain("tlsConfig");
  });

  test("chart preserves backward compatibility when serviceMonitor.scheme=http", async () => {
    const tempDir = mkdtempSync(join(tmpdir(), "msars-http-"));
    try {
      const valuesFile = join(tempDir, "values.yaml");
      writeFileSync(
        valuesFile,
        `
serviceMonitor:
  enabled: true
  scheme: http
  bearerTokenFile: null
  tlsConfig: null
`,
      );
      const manifest = helmTemplate(valuesFile);
      expect(manifest).toContain("kind: ServiceMonitor");
      expect(manifest).toContain("scheme: http");
      expect(manifest).not.toContain("bearerTokenFile:");
      expect(manifest).not.toContain("tlsConfig:");
    } finally {
      rmSync(tempDir, { recursive: true, force: true });
    }
  });
});
