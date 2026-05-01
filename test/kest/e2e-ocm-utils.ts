import { expect } from "bun:test";
import type { Cluster, K8sResource, Namespace, Scenario } from "@appthrust/kest";

type Condition = {
  type: string;
  status: string;
  reason?: string;
};

type PlacementDecision = K8sResource & {
  status?: {
    decisions?: Array<{ clusterName: string; reason?: string }>;
  };
};

type ClusterProfile = K8sResource;

type ManagedCluster = K8sResource & {
  spec?: {
    managedClusterClientConfigs?: Array<{
      url?: string;
      caBundle?: string;
    }>;
  };
};

type E2ESettings = {
  kubeconfig: string;
  hubName: string;
  spoke1Name: string;
  spoke2Name: string;
};

type SpokeFixture = {
  name: string;
  cluster: Cluster;
  appNamespace: Namespace;
  driftNamespace: Namespace;
  managedServiceAccountNamespace: Namespace;
  hubNamespace: Namespace;
};

export type OcmE2EEnvironment = {
  hub: Cluster;
  tenant: Namespace;
  spokes: readonly [SpokeFixture, SpokeFixture];
};

const clusterSetName = "msars-e2e";
const tenantNamespace = "msars-e2e";
export const remoteAppNamespace = "app-a";
export const remoteDriftNamespace = "app-b";
export const serviceAccountNamespace = "open-cluster-management-agent-addon";
export const placementName = "prod-clusters";

function requireSettings(): E2ESettings {
  const kubeconfig = process.env.KUBECONFIG;
  if (!kubeconfig) {
    throw new Error("KUBECONFIG is required");
  }

  return {
    kubeconfig,
    hubName: process.env.E2E_HUB_CLUSTER ?? "msars-e2e-hub",
    spoke1Name: process.env.E2E_SPOKE1_CLUSTER ?? "msars-e2e-spoke1",
    spoke2Name: process.env.E2E_SPOKE2_CLUSTER ?? "msars-e2e-spoke2",
  };
}

function resourceNames(items: ReadonlyArray<{ metadata: { name: string } }>): Array<string> {
  return items.map((item) => item.metadata.name).sort();
}

function selectedClusterNames(decisions: ReadonlyArray<PlacementDecision>): Array<string> {
  const selected = new Set<string>();
  for (const decision of decisions) {
    if (decision.metadata.labels?.["cluster.open-cluster-management.io/placement"] !== placementName) {
      continue;
    }
    for (const item of decision.status?.decisions ?? []) {
      selected.add(item.clusterName);
    }
  }
  return [...selected].sort();
}

export function conditionStatus(
  resource: { status?: { conditions?: Condition[] }; conditions?: Condition[] },
  type: string,
): string | undefined {
  return (resource.status?.conditions ?? resource.conditions)?.find((condition) => condition.type === type)?.status;
}

export function wait(timeout: string) {
  return { timeout, interval: "5s", stallTimeout: "0s" };
}

export async function useProvisionedOcmEnvironment(s: Scenario): Promise<OcmE2EEnvironment> {
  const settings = requireSettings();
  const hub = await s.useCluster({ context: `kind-${settings.hubName}`, kubeconfig: settings.kubeconfig });
  const spoke1 = await s.useCluster({ context: `kind-${settings.spoke1Name}`, kubeconfig: settings.kubeconfig });
  const spoke2 = await s.useCluster({ context: `kind-${settings.spoke2Name}`, kubeconfig: settings.kubeconfig });

  const tenant = await hub.newNamespace(tenantNamespace, { timeout: "60s" });
  const spoke1App = await spoke1.newNamespace(remoteAppNamespace, { timeout: "60s" });
  const spoke2App = await spoke2.newNamespace(remoteAppNamespace, { timeout: "60s" });
  const spoke1Drift = await spoke1.newNamespace(remoteDriftNamespace, { timeout: "60s" });
  const spoke2Drift = await spoke2.newNamespace(remoteDriftNamespace, { timeout: "60s" });
  for (const cluster of [spoke1, spoke2]) {
    await cluster.label(
      {
        apiVersion: "v1",
        kind: "Namespace",
        name: remoteAppNamespace,
        labels: {
          "workload-identity.appthrust.io/profile": "aws",
        },
        overwrite: true,
      },
      { timeout: "60s" },
    );
  }
  const spoke1Msa = await spoke1.useNamespace(serviceAccountNamespace, wait("300s"));
  const spoke2Msa = await spoke2.useNamespace(serviceAccountNamespace, wait("300s"));
  const hubSpoke1 = await hub.useNamespace(settings.spoke1Name, wait("300s"));
  const hubSpoke2 = await hub.useNamespace(settings.spoke2Name, wait("300s"));

  return {
    hub,
    tenant,
    spokes: [
      {
        name: settings.spoke1Name,
        cluster: spoke1,
        appNamespace: spoke1App,
        driftNamespace: spoke1Drift,
        managedServiceAccountNamespace: spoke1Msa,
        hubNamespace: hubSpoke1,
      },
      {
        name: settings.spoke2Name,
        cluster: spoke2,
        appNamespace: spoke2App,
        driftNamespace: spoke2Drift,
        managedServiceAccountNamespace: spoke2Msa,
        hubNamespace: hubSpoke2,
      },
    ],
  };
}

export async function configureOcmPlacement(env: OcmE2EEnvironment): Promise<void> {
  await env.hub.apply({
    apiVersion: "cluster.open-cluster-management.io/v1beta2",
    kind: "ManagedClusterSet",
    metadata: { name: clusterSetName },
    spec: {
      clusterSelector: {
        selectorType: "ExclusiveClusterSetLabel",
      },
    },
  });

  for (const spoke of env.spokes) {
    await env.hub.label(
      {
        apiVersion: "cluster.open-cluster-management.io/v1",
        kind: "ManagedCluster",
        name: spoke.name,
        labels: {
          "cluster.open-cluster-management.io/clusterset": clusterSetName,
        },
        overwrite: true,
      },
      { timeout: "60s" },
    );
  }

  await env.tenant.apply({
    apiVersion: "cluster.open-cluster-management.io/v1beta2",
    kind: "ManagedClusterSetBinding",
    metadata: { name: clusterSetName },
    spec: { clusterSet: clusterSetName },
  });
  await env.tenant.apply({
    apiVersion: "cluster.open-cluster-management.io/v1beta1",
    kind: "Placement",
    metadata: { name: placementName },
    spec: {
      numberOfClusters: env.spokes.length,
      clusterSets: [clusterSetName],
      tolerations: [
        { key: "cluster.open-cluster-management.io/unreachable", operator: "Exists" },
        { key: "cluster.open-cluster-management.io/unavailable", operator: "Exists" },
      ],
    },
  });
}

export async function assertOcmPlacementResolved(env: OcmE2EEnvironment): Promise<void> {
  const expectedSpokes = env.spokes.map((spoke) => spoke.name).sort();

  await env.tenant.assertList<ClusterProfile>(
    {
      apiVersion: "multicluster.x-k8s.io/v1alpha1",
      kind: "ClusterProfile",
      test() {
        expect(resourceNames(this)).toEqual(expectedSpokes);
        for (const profile of this) {
          expect(profile.metadata.labels?.["open-cluster-management.io/cluster-name"]).toBe(profile.metadata.name);
          expect(profile.metadata.labels?.["x-k8s.io/cluster-manager"]).toBe("open-cluster-management");
        }
      },
    },
    wait("300s"),
  );

  await publishClusterProfileAccessProviders(env);

  await env.tenant.assertList<PlacementDecision>(
    {
      apiVersion: "cluster.open-cluster-management.io/v1beta1",
      kind: "PlacementDecision",
      test() {
        expect(selectedClusterNames(this)).toEqual(expectedSpokes);
      },
    },
    wait("300s"),
  );
}

async function publishClusterProfileAccessProviders(env: OcmE2EEnvironment): Promise<void> {
  for (const spoke of env.spokes) {
    const managedCluster = await env.hub.assert<ManagedCluster>(
      {
        apiVersion: "cluster.open-cluster-management.io/v1",
        kind: "ManagedCluster",
        name: spoke.name,
        test() {
          expect(this.spec?.managedClusterClientConfigs?.[0]?.url).toBeTruthy();
          expect(this.spec?.managedClusterClientConfigs?.[0]?.caBundle).toBeTruthy();
        },
      },
      wait("300s"),
    );
    const clientConfig = managedCluster.spec?.managedClusterClientConfigs?.[0];
    await env.tenant.applyStatus({
      apiVersion: "multicluster.x-k8s.io/v1alpha1",
      kind: "ClusterProfile",
      metadata: { name: spoke.name },
      status: {
        accessProviders: [
          {
            name: "open-cluster-management",
            cluster: {
              server: clientConfig?.url,
              "certificate-authority-data": clientConfig?.caBundle,
              extensions: [
                {
                  name: "client.authentication.k8s.io/exec",
                  extension: {
                    clusterName: spoke.name,
                  },
                },
              ],
            },
          },
        ],
      },
    });
  }
}
