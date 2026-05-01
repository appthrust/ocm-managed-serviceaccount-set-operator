import { expect } from "bun:test";
import { test, type K8sResource } from "@appthrust/kest";
import { createHash } from "node:crypto";
import {
  assertOcmPlacementResolved,
  conditionStatus,
  configureOcmPlacement,
  placementName,
  remoteAppNamespace,
  remoteDriftNamespace,
  serviceAccountNamespace,
  useProvisionedOcmEnvironment,
  wait,
} from "./e2e-ocm-utils";

type Condition = {
  type: string;
  status: string;
  reason?: string;
};

type ManagedServiceAccountReplicaSet = K8sResource & {
  status?: {
    selectedClusterCount?: number;
    readyClusterCount?: number;
    summary?: {
      desiredTotal?: number;
      total?: number;
      applied?: number;
      available?: number;
      updated?: number;
    };
    controllerAccess?: {
      desiredClusterCount?: number;
      readyClusterCount?: number;
      conditions?: Condition[];
    };
    conditions?: Condition[];
  };
};

type ManagedServiceAccount = K8sResource & {
  status?: {
    tokenSecretRef?: { name?: string };
    conditions?: Condition[];
  };
};

type ManifestWork = K8sResource & {
  status?: {
    conditions?: Condition[];
  };
};

type Secret = K8sResource & { data?: Record<string, string> };

const replicaSetName = "web";
const rbacName = "web-configmaps";
const fixedRoleName = "web-fixed-secrets";
const clusterRoleName = "web-nodes";

function stableHash(value: string): string {
  return createHash("sha256").update(value).digest("hex").slice(0, 10);
}

test(
  "ManagedServiceAccountReplicaSet fans out through real OCM to two spokes",
  async (s) => {
    s.given("a provisioned OCM hub with two accepted spokes and the managed-serviceaccount addon");
    const env = await useProvisionedOcmEnvironment(s);

    s.when("the setup binds both spokes to an OCM Placement");
    await configureOcmPlacement(env);

    s.then("OCM resolves the Placement to ClusterProfiles and PlacementDecisions");
    await assertOcmPlacementResolved(env);

    s.when("the controller receives a ManagedServiceAccountReplicaSet for that Placement");
    await env.tenant.apply({
      apiVersion: "authentication.appthrust.io/v1alpha1",
      kind: "ManagedServiceAccountReplicaSet",
      metadata: { name: replicaSetName },
      spec: {
        placementRefs: [
          {
            name: placementName,
          },
        ],
        template: {
          metadata: {
            name: replicaSetName,
            namespace: serviceAccountNamespace,
            labels: { "e2e.appthrust.io/scenario": "two-spokes" },
          },
          spec: {
            rotation: { enabled: true },
          },
        },
        rbac: {
          grants: [
            {
              id: "web-configmaps",
              type: "Role",
              forEachNamespace: {
                selector: {
                  matchLabels: {
                    "workload-identity.appthrust.io/profile": "aws",
                  },
                },
              },
              metadata: {
                name: rbacName,
              },
              rules: [
                {
                  apiGroups: [""],
                  resources: ["configmaps"],
                  verbs: ["get", "list"],
                },
              ],
            },
            {
              id: "web-nodes",
              type: "ClusterRole",
              metadata: { name: clusterRoleName },
              rules: [
                {
                  apiGroups: [""],
                  resources: ["nodes"],
                  verbs: ["get"],
                },
              ],
            },
            {
              id: "fixed-secrets",
              type: "Role",
              forEachNamespace: {
                names: [serviceAccountNamespace],
              },
              metadata: { name: fixedRoleName },
              rules: [
                {
                  apiGroups: [""],
                  resources: ["secrets"],
                  verbs: ["get"],
                },
              ],
            },
          ],
        },
      },
    });

    s.then("the controller creates OCM ManagedServiceAccounts and remote RBAC ManifestWorks");
    for (const spoke of env.spokes) {
      await spoke.hubNamespace.assert<ManagedServiceAccount>(
        {
          apiVersion: "authentication.open-cluster-management.io/v1beta1",
          kind: "ManagedServiceAccount",
          name: replicaSetName,
          test() {
            expect(this.metadata.labels?.["authentication.open-cluster-management.io/sync-to-clusterprofile"]).toBe("true");
            expect(this.metadata.labels?.["authentication.appthrust.io/set-name"]).toBe(replicaSetName);
          },
        },
        wait("180s"),
      );
      await spoke.hubNamespace.assert<ManifestWork>(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-cluster`,
          test() {
            expect(this.metadata.labels?.["authentication.appthrust.io/set-name"]).toBe(replicaSetName);
            expect(this.metadata.labels?.["authentication.appthrust.io/slice-type"]).toBe("workload-rbac-cluster");
          },
        },
        wait("180s"),
      );
      await spoke.hubNamespace.assert<ManifestWork>(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-ns-${stableHash(remoteAppNamespace)}`,
          test() {
            expect(this.metadata.labels?.["authentication.appthrust.io/set-name"]).toBe(replicaSetName);
            expect(this.metadata.labels?.["authentication.appthrust.io/slice-type"]).toBe("workload-rbac-namespace");
            expect(this.metadata.annotations?.["authentication.appthrust.io/target-namespace"]).toBe(remoteAppNamespace);
          },
        },
        wait("180s"),
      );
      await spoke.hubNamespace.assert<ManifestWork>(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-ns-${stableHash(serviceAccountNamespace)}`,
          test() {
            expect(this.metadata.labels?.["authentication.appthrust.io/set-name"]).toBe(replicaSetName);
            expect(this.metadata.labels?.["authentication.appthrust.io/slice-type"]).toBe("workload-rbac-namespace");
            expect(this.metadata.annotations?.["authentication.appthrust.io/target-namespace"]).toBe(serviceAccountNamespace);
          },
        },
        wait("180s"),
      );
    }

    s.then("the OCM work agent applies the typed RBAC on both spokes");
    for (const spoke of env.spokes) {
      await spoke.cluster.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "ClusterRole",
          name: clusterRoleName,
          test() {
            expect(this.metadata.name).toBe(clusterRoleName);
          },
        },
        wait("300s"),
      );
      await spoke.cluster.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "ClusterRoleBinding",
          name: clusterRoleName,
          test() {
            expect(this.metadata.name).toBe(clusterRoleName);
          },
        },
        wait("300s"),
      );
    }
    for (const spoke of env.spokes) {
      await spoke.appNamespace.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: rbacName,
          test() {
            expect(this.metadata.name).toBe(rbacName);
          },
        },
        wait("300s"),
      );
      await spoke.appNamespace.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: rbacName,
          test() {
            expect(this.metadata.name).toBe(rbacName);
          },
        },
        wait("300s"),
      );
    }
    for (const spoke of env.spokes) {
      await spoke.managedServiceAccountNamespace.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: fixedRoleName,
          test() {
            expect(this.metadata.name).toBe(fixedRoleName);
          },
        },
        wait("300s"),
      );
      await spoke.managedServiceAccountNamespace.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: fixedRoleName,
          test() {
            expect(this.metadata.name).toBe(fixedRoleName);
          },
        },
        wait("300s"),
      );
    }

    s.then("the managed-serviceaccount addon reports tokens and creates remote ServiceAccounts");
    for (const spoke of env.spokes) {
      await spoke.hubNamespace.assert<ManagedServiceAccount>(
        {
          apiVersion: "authentication.open-cluster-management.io/v1beta1",
          kind: "ManagedServiceAccount",
          name: replicaSetName,
          test() {
            expect(conditionStatus(this, "TokenReported") ?? conditionStatus(this, "SecretCreated")).toBe("True");
            expect(this.status?.tokenSecretRef?.name).toBe(replicaSetName);
          },
        },
        wait("600s"),
      );
      await spoke.hubNamespace.assert<Secret>(
        {
          apiVersion: "v1",
          kind: "Secret",
          name: replicaSetName,
          test() {
            expect(this.data?.token).toBeTruthy();
            expect(this.data?.["ca.crt"]).toBeTruthy();
          },
        },
        wait("300s"),
      );
      await env.tenant.assert<Secret>(
        {
          apiVersion: "v1",
          kind: "Secret",
          name: `${spoke.name}-${replicaSetName}`,
          test() {
            expect(this.metadata.labels?.["authentication.open-cluster-management.io/synced-from"]).toBe(
              `${spoke.name}-${replicaSetName}`,
            );
            expect(this.data?.token).toBeTruthy();
          },
        },
        wait("300s"),
      );
      await spoke.managedServiceAccountNamespace.assert<K8sResource>(
        {
          apiVersion: "v1",
          kind: "ServiceAccount",
          name: replicaSetName,
          test() {
            expect(this.metadata.name).toBe(replicaSetName);
          },
        },
        wait("300s"),
      );
    }

    s.then("the replica set status reflects both selected spokes as ready");
    await env.tenant.assert<ManagedServiceAccountReplicaSet>(
      {
        apiVersion: "authentication.appthrust.io/v1alpha1",
        kind: "ManagedServiceAccountReplicaSet",
        name: replicaSetName,
        test() {
          expect(this.status?.selectedClusterCount).toBe(env.spokes.length);
          expect(this.status?.readyClusterCount).toBe(env.spokes.length);
          expect(this.status?.summary?.desiredTotal).toBe(env.spokes.length * 3);
          expect(this.status?.summary?.total).toBe(env.spokes.length * 3);
          expect(this.status?.summary?.applied).toBe(env.spokes.length * 3);
          expect(this.status?.summary?.available).toBe(env.spokes.length * 3);
          expect(this.status?.summary?.updated).toBe(env.spokes.length * 3);
          expect(this.status?.controllerAccess?.desiredClusterCount).toBe(env.spokes.length);
          expect(this.status?.controllerAccess?.readyClusterCount).toBe(env.spokes.length);
          expect(conditionStatus(this.status?.controllerAccess ?? {}, "Ready")).toBe("True");
          expect(conditionStatus(this, "Ready")).toBe("True");
          expect(conditionStatus(this, "CleanupBlocked")).not.toBe("True");
        },
      },
      wait("600s"),
    );

    s.when("a second namespace starts matching the selector");
    for (const spoke of env.spokes) {
      await spoke.cluster.label(
        {
          apiVersion: "v1",
          kind: "Namespace",
          name: remoteDriftNamespace,
          labels: {
            "workload-identity.appthrust.io/profile": "aws",
          },
          overwrite: true,
        },
        { timeout: "60s" },
      );
    }

    s.then("the controller creates a new namespace-scoped slice and remote RBAC");
    for (const spoke of env.spokes) {
      await spoke.hubNamespace.assert<ManifestWork>(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-ns-${stableHash(remoteDriftNamespace)}`,
          test() {
            expect(this.metadata.labels?.["authentication.appthrust.io/slice-type"]).toBe("workload-rbac-namespace");
            expect(this.metadata.annotations?.["authentication.appthrust.io/target-namespace"]).toBe(remoteDriftNamespace);
          },
        },
        wait("300s"),
      );
      await spoke.driftNamespace.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: rbacName,
          test() {
            expect(this.metadata.name).toBe(rbacName);
          },
        },
        wait("300s"),
      );
      await spoke.driftNamespace.assert<K8sResource>(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: rbacName,
          test() {
            expect(this.metadata.name).toBe(rbacName);
          },
        },
        wait("300s"),
      );
    }
    await env.tenant.assert<ManagedServiceAccountReplicaSet>(
      {
        apiVersion: "authentication.appthrust.io/v1alpha1",
        kind: "ManagedServiceAccountReplicaSet",
        name: replicaSetName,
        test() {
          expect(this.status?.readyClusterCount).toBe(env.spokes.length);
          expect(this.status?.summary?.desiredTotal).toBe(env.spokes.length * 4);
          expect(this.status?.summary?.available).toBe(env.spokes.length * 4);
        },
      },
      wait("600s"),
    );

    s.when("the original namespace stops matching the selector");
    for (const spoke of env.spokes) {
      await spoke.cluster.label(
        {
          apiVersion: "v1",
          kind: "Namespace",
          name: remoteAppNamespace,
          labels: {
            "workload-identity.appthrust.io/profile": "disabled",
          },
          overwrite: true,
        },
        { timeout: "60s" },
      );
    }

    s.then("the stale namespace-scoped slice and its remote RBAC disappear");
    for (const spoke of env.spokes) {
      await spoke.hubNamespace.assertAbsence(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-ns-${stableHash(remoteAppNamespace)}`,
        },
        wait("300s"),
      );
      await spoke.appNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: rbacName,
        },
        wait("300s"),
      );
      await spoke.appNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: rbacName,
        },
        wait("300s"),
      );
    }
    await env.tenant.assert<ManagedServiceAccountReplicaSet>(
      {
        apiVersion: "authentication.appthrust.io/v1alpha1",
        kind: "ManagedServiceAccountReplicaSet",
        name: replicaSetName,
        test() {
          expect(this.status?.readyClusterCount).toBe(env.spokes.length);
          expect(this.status?.summary?.desiredTotal).toBe(env.spokes.length * 3);
          expect(this.status?.summary?.available).toBe(env.spokes.length * 3);
        },
      },
      wait("600s"),
    );

    s.when("the ManagedServiceAccountReplicaSet is deleted");
    await env.tenant.delete(
      {
        apiVersion: "authentication.appthrust.io/v1alpha1",
        kind: "ManagedServiceAccountReplicaSet",
        name: replicaSetName,
      },
      wait("60s"),
    );

    s.then("the controller removes remote RBAC before generated ManagedServiceAccounts disappear");
    for (const spoke of env.spokes) {
      await spoke.appNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: rbacName,
        },
        wait("300s"),
      );
      await spoke.appNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: rbacName,
        },
        wait("300s"),
      );
    }
    for (const spoke of env.spokes) {
      await spoke.driftNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: rbacName,
        },
        wait("300s"),
      );
      await spoke.driftNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: rbacName,
        },
        wait("300s"),
      );
      await spoke.managedServiceAccountNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "RoleBinding",
          name: fixedRoleName,
        },
        wait("300s"),
      );
      await spoke.managedServiceAccountNamespace.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "Role",
          name: fixedRoleName,
        },
        wait("300s"),
      );
    }
    for (const spoke of env.spokes) {
      await spoke.cluster.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "ClusterRoleBinding",
          name: clusterRoleName,
        },
        wait("300s"),
      );
      await spoke.cluster.assertAbsence(
        {
          apiVersion: "rbac.authorization.k8s.io/v1",
          kind: "ClusterRole",
          name: clusterRoleName,
        },
        wait("300s"),
      );
    }
    for (const spoke of env.spokes) {
      await spoke.hubNamespace.assertAbsence(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-cluster`,
        },
        wait("300s"),
      );
      await spoke.hubNamespace.assertAbsence(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-ns-${stableHash(remoteDriftNamespace)}`,
        },
        wait("300s"),
      );
      await spoke.hubNamespace.assertAbsence(
        {
          apiVersion: "work.open-cluster-management.io/v1",
          kind: "ManifestWork",
          name: `${replicaSetName}-rbac-ns-${stableHash(serviceAccountNamespace)}`,
        },
        wait("300s"),
      );
      await spoke.hubNamespace.assertAbsence(
        {
          apiVersion: "authentication.open-cluster-management.io/v1beta1",
          kind: "ManagedServiceAccount",
          name: replicaSetName,
        },
        wait("300s"),
      );
    }
  },
  { timeout: "15m" },
);
