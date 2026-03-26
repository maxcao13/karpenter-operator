# Karpenter Operator — Architecture

This document describes the internal architecture of the `karpenter-operator`,
a CVO-managed ClusterOperator that deploys and manages
[Karpenter](https://karpenter.sh/) on self-managed OpenShift clusters.

---

## High-level overview

The system has two binaries running in the `openshift-karpenter` namespace:

| Component | What it is | Who manages it |
|---|---|---|
| **karpenter-operator** | OpenShift-specific lifecycle controller | CVO applies the Deployment from `install/` |
| **karpenter** (operand) | Upstream Karpenter node autoscaler | The operator creates and owns its Deployment |

The operator's job is to bridge the gap between upstream Karpenter (designed for
EKS) and OpenShift. It handles infrastructure discovery, RHCOS-aware node
bootstrapping, CSR approval for nodes that bypass Machine API, MCO integration,
and ClusterOperator status reporting.

```
┌──────────────────────────────────────────────────────────────────┐
│                        openshift-karpenter                       │
│                                                                  │
│  ┌──────────────────────┐       ┌─────────────────────────────┐  │
│  │  karpenter-operator  │       │  karpenter (operand)        │  │
│  │                      │       │                             │  │
│  │  - deployment ctrl   │──────>│  Upstream Karpenter with    │  │
│  │  - nodeclass ctrl    │ owns  │  AWS provider, manages      │  │
│  │  - machine approver  │       │  NodePools, NodeClaims,     │  │
│  │  - MCP ctrl          │       │  EC2 instances              │  │
│  │  - status reporter   │       └─────────────────────────────┘  │
│  └──────────────────────┘                                        │
└──────────────────────────────────────────────────────────────────┘
```

---

## Startup and infrastructure discovery

When the operator starts (`pkg/operator/operator.go`), it:

1. Reads the `Infrastructure` CR (`config.openshift.io/v1`) to discover the
   cloud platform, region, cluster name, and internal API endpoint.
2. Calls `cloudprovider.GetCloudProvider()` which switches on
   `status.platformStatus.type` (e.g. `AWS`) and returns a cloud-specific
   `CloudProvider` implementation.
3. The `CloudProvider` registers its CRD types with the scheme (e.g.
   `EC2NodeClass`), initializes cloud API clients, and provides configuration
   to all downstream controllers.

This single discovery step means no cloud-specific environment variables
(`AWS_REGION`, etc.) need to be baked into the operator's static deployment
manifest — the operator figures everything out at runtime.

---

## Controllers

### 1. Deployment controller (`pkg/controllers/deployment/`)

Reconciles the operand: the upstream Karpenter `Deployment`, `ServiceAccount`,
`Role`, `RoleBinding`, `ClusterRole`, and `ClusterRoleBinding`.

All resources are built as Go structs (no YAML templates) and managed with
`controllerutil.CreateOrUpdate`. The operand Deployment is owned by the
operator Deployment via `ownerReference`, so Kubernetes garbage collection
removes the operand if the operator is deleted.

**Credential gating**: Before creating the operand Deployment, the controller
checks that the operand's cloud credentials Secret (provisioned by CCO from the
`CredentialsRequest`) exists. If missing, it logs and requeues every 10 seconds.
A watch on the Secret triggers immediate reconciliation when CCO fulfills it.
This prevents the operand from crash-looping on startup while credentials
propagate.

**Cloud-specific injection**: The controller calls
`CloudProvider.OperandConfig()` to get cloud-specific environment variables,
RBAC rules, volumes, and volume mounts. For AWS, this injects
`AWS_REGION`, `AWS_SHARED_CREDENTIALS_FILE`, and the credentials Secret
volume. This keeps the deployment controller fully generic.

**Init container**: The operand pod includes a `check-credentials` init
container that runs the operator binary with the `check-credentials`
subcommand. It shares the operand's credential volume mounts and cloud env
vars, and blocks until the cloud API accepts the credentials. This prevents
the operand from crash-looping due to credential propagation delays on first
install.

### 2. NodeClass controller (`pkg/controllers/nodeclass/`)

A thin generic wrapper that delegates to `CloudProvider.ReconcileDefaultNodeClass()`.
Creates and maintains a well-known NodeClass named `default` with turnkey
infrastructure defaults so that users only need to create a `NodePool` to start
autoscaling.

On AWS, this produces an `EC2NodeClass` whose fields are discovered from
the cluster:

| Field | Discovery source |
|---|---|
| AMI ID | Worker MachineSet `providerSpec.ami.id` |
| Instance profile | Worker MachineSet `providerSpec.iamInstanceProfile.id` |
| Security groups | Worker MachineSet `providerSpec.securityGroups[].filters` |
| Subnets | Tag-based selector: `kubernetes.io/cluster/<infraName>: *` |
| Block devices | Worker MachineSet `providerSpec.blockDevices`, or 120Gi gp3 default |
| User data | `worker-user-data` Secret in `openshift-machine-api`, rewritten for the karpenter MachineConfigPool (see below) |

The controller falls back to naming conventions derived from
`Infrastructure.status.infrastructureName` if no worker MachineSets exist.

### 3. Machine approver (`pkg/controllers/machineapprover/`)

Karpenter provisions EC2 instances directly — it does not create Machine API
`Machine` objects. OpenShift's built-in `cluster-machine-approver` only
approves CSRs for nodes that have a corresponding Machine, so Karpenter-
provisioned nodes would have their certificate signing requests stuck in
`Pending` indefinitely.

The karpenter machine approver fills this gap. It watches pending CSRs and
handles two signer types:

| Signer | Purpose | Authorization check |
|---|---|---|
| `kubernetes.io/kube-apiserver-client-kubelet` | Bootstrap client cert (from `node-bootstrapper` SA) | Parse CSR common name → extract node name → find unassigned NodeClaims → call `DescribeInstances` → verify DNS name match |
| `kubernetes.io/kubelet-serving` | Kubelet HTTPS serving cert (from `system:node:*`) | Extract node name from CSR username → find NodeClaim with matching `status.nodeName` → call `DescribeInstances` → verify DNS name match |

The check is: if a pending CSR's requested node name matches the private DNS
name of an EC2 instance that backs a Karpenter NodeClaim, approve it. This
prevents rogue CSR approval by tying approval to a real cloud instance owned
by Karpenter.

**Early exit**: If no NodeClaims exist at all, the controller skips the CSR
immediately — it belongs to a standard Machine API node and the built-in
approver will handle it. This avoids unnecessary AWS API calls.

**Credential readiness**: Before processing any CSR, the controller checks that
the operator's cloud credentials file exists and has content. If not, it
requeues after 10 seconds instead of error-looping with AWS auth failures.

### 4. MachineConfigPool controller (`pkg/controllers/machineconfigpool/`)

This controller integrates Karpenter-provisioned nodes with OpenShift's Machine
Config Operator (MCO). It exists because of a fundamental difference between
how EKS and OpenShift handle node configuration.

#### Background: Why Karpenter needs MCO integration

On EKS, Karpenter injects kubelet configuration (including startup taints)
directly into the EC2 launch template's user data via a shell bootstrap script.
On OpenShift, node configuration is managed declaratively through the MCO
pipeline:

```
KubeletConfig CR ──> MCO renders ──> MachineConfig ──> MCS serves to nodes via Ignition
```

RHCOS nodes fetch their full configuration from the Machine Config Server (MCS)
at boot time. The MCS endpoint path determines which MachineConfigPool's
rendered configuration the node receives (e.g. `/config/worker` for the worker
pool).

#### What the controller does

For each NodeClass (e.g. `EC2NodeClass` named `default`), the controller
creates:

1. **A `MachineConfigPool`** named `karpenter-<nodeclass-name>` (e.g.
   `karpenter-default`) that:
   - Selects nodes via the cloud-specific NodeClass label (e.g.
     `karpenter.k8s.aws/ec2nodeclass: default` — applied automatically by
     Karpenter to every node it provisions from that NodeClass)
   - Inherits all `worker` MachineConfigs via `machineConfigSelector` with
     `matchExpressions: [{key: machineconfiguration.openshift.io/role, operator: In, values: [worker, karpenter-default]}]`
   - Is **paused** (`spec.paused: true`) — MCO will not attempt to drain or
     update Karpenter-managed nodes, since Karpenter's drift mechanism handles
     node lifecycle

2. **A shared `KubeletConfig`** named `set-karpenter-taint` that:
   - Targets all karpenter MCPs via the label `karpenter-operator.openshift.io/managed: "true"`
   - Configures `registerWithTaints: [{key: karpenter.sh/unregistered, value: "true", effect: NoExecute}]`

MCO renders this KubeletConfig into a MachineConfig for each matching MCP. MCS
then serves the rendered configuration (including the startup taint) at
`/config/karpenter-<name>`.

#### The startup taint

Karpenter requires new nodes to register with the `karpenter.sh/unregistered:NoExecute`
taint. Karpenter removes this taint after it successfully links the NodeClaim to
the Node. Without it, there is a race condition where pods could be scheduled to
a node before Karpenter has finished initializing it.

On EKS, this taint is injected via the kubelet `--register-with-taints` flag in
the bootstrap script. On OpenShift, it is configured declaratively via the
KubeletConfig → MCO → MCS pipeline described above.

#### Ignition rewriting

The NodeClass controller (specifically the AWS implementation in
`pkg/cloudprovider/aws/nodeclass.go`) rewrites the Ignition user data before
setting it on the EC2NodeClass. The original `worker-user-data` secret contains
an Ignition pointer config that tells the node to fetch its full configuration
from MCS at `/config/worker`. The rewriter changes this to
`/config/karpenter-<nodeclass-name>`, ensuring new Karpenter nodes receive the
karpenter MCP's rendered configuration (which includes the startup taint).

```
Original:  https://api-int.<cluster>:22623/config/worker
Rewritten: https://api-int.<cluster>:22623/config/karpenter-default
```

#### Future: user-configurable node tuning

Because Karpenter nodes now have their own MachineConfigPool, the same MCO
machinery that OpenShift administrators already use for node tuning becomes
available for Karpenter-provisioned nodes:

- **KubeletConfig** — Tune kubelet parameters (eviction thresholds, max pods,
  CPU manager policy, etc.) per NodeClass by targeting the corresponding
  karpenter MCP.
- **MachineConfig** — Inject custom systemd units, files, kernel arguments, or
  other OS-level configuration into Karpenter nodes.
- **ContainerRuntimeConfig** — Customize CRI-O settings.
- **Node Tuning Operator / TuneD profiles** — Apply performance tuning profiles
  that select nodes by MCP membership.
- **Per-NodeClass differentiation** — Since each NodeClass gets its own MCP,
  different node classes can have different OS/kubelet configurations. For
  example, a `gpu` EC2NodeClass could have a dedicated MCP with GPU-specific
  kernel parameters, while the `default` NodeClass uses standard worker
  settings.

The `paused: true` setting on karpenter MCPs means MCO will not roll out
configuration changes by draining nodes. Instead, Karpenter's drift detection
can be used: when the rendered MachineConfig changes, the MCS will serve
updated Ignition to new nodes, and Karpenter drift can replace existing nodes
to pick up the new configuration. Unpausing the MCP is also an option if
in-place MCO updates are preferred.

---

## Multi-cloud architecture

The operator follows the
[cluster-cloud-controller-manager-operator](https://github.com/openshift/cluster-cloud-controller-manager-operator)
pattern. All cloud-specific logic is behind the `CloudProvider` interface
(`pkg/cloudprovider/cloud.go`):

```go
type CloudProvider interface {
    AddToScheme(s *runtime.Scheme) error
    ReconcileDefaultNodeClass(ctx context.Context, c client.Client, infraName string) error
    NodeClassObject() client.Object
    GetInstanceDNSNames(ctx context.Context, nodeClaims []karpenterv1.NodeClaim) ([]string, error)
    AuthorizeCSR(ctx context.Context, csr *certificatesv1.CertificateSigningRequest, c client.Client) (bool, error)
    OperandConfig() types.OperandCloudConfig
    RelatedObjects() []configv1.ObjectReference
    Region() string
    NodeClassLabel() string
}
```

`GetCloudProvider()` switches on the Infrastructure CR's platform type and
returns the appropriate implementation. Currently only AWS is implemented
(`pkg/cloudprovider/aws/`). Adding a new cloud provider requires:

1. Implementing `CloudProvider` in `pkg/cloudprovider/<provider>/`
2. Adding a `case` to the switch in `pkg/cloudprovider/cloud.go`
3. Adding `install/00_credentials-request-<cloud>.yaml` and CRD manifests
4. Adding the operand image to `image-references`
5. Adding `CheckCredentials` in `pkg/cloudprovider/<provider>/credentials.go` and
   a `case` in `cmd/main.go:runCheckCredentials()` keyed on a canonical env var

Generic controllers never import cloud-provider packages directly.

---

## Credentials

Two separate `CredentialsRequest` resources are used, each granting
least-privilege IAM permissions:

| CredentialsRequest | Secret | Used by | Permissions |
|---|---|---|---|
| `karpenter-operator-cloud-credentials` | Same name in `openshift-karpenter` | Operator (machine approver) | `ec2:DescribeInstances` (CSR verification) |
| `karpenter-cloud-credentials` | Same name in `openshift-karpenter` | Operand (Karpenter) | `ec2:RunInstances`, `ec2:CreateFleet`, `ec2:DescribeInstanceTypes`, IAM instance profile management, SSM, pricing |

Both are `optional: true` volume mounts on the operator pod. The operand's
credential volume is not optional — if the Secret is missing, the pod stays in
`ContainerCreating`. The deployment controller gates on the Secret's existence
before creating the operand Deployment.

### Credential readiness init container

Even after CCO provisions the credentials Secret, newly created cloud
credentials may take several seconds to propagate (e.g. AWS IAM eventual
consistency). Without mitigation, the operand would crash on its startup
connectivity check and restart.

The operand Deployment includes an init container (`check-credentials`) that
runs the operator binary with the `check-credentials` subcommand. It polls the
cloud provider API in a retry loop until credentials are accepted, then exits.
The main Karpenter container only starts once the init container succeeds.

The subcommand detects the cloud provider from a canonical set of environment
variables injected by the operator (e.g. `AWS_REGION` for AWS). Each provider
implements its own readiness check in `pkg/cloudprovider/<provider>/credentials.go`.
For AWS, this is a lightweight `DescribeInstanceTypes` call.

---

## ClusterOperator status reporting

The `StatusReporter` (`pkg/operator/status.go`) runs as a `manager.Runnable`,
polling the operand Deployment's health and updating the `karpenter`
ClusterOperator CR:

| Condition | Meaning |
|---|---|
| `Available=True` | Operand Deployment has expected ready replicas |
| `Progressing=True` | Operand is rolling out or not yet ready |
| `Degraded=True` | Multiple consecutive health check failures (threshold prevents flapping) |
| `Upgradeable=True` | Always true (for now) |

During upgrades, the operator reports the **previous** version until all
operands are fully rolled out at the new version, following the CVO version
reporting protocol.

---

## RBAC

Two ClusterRoles exist:

1. **`karpenter-operator`** (static manifest in `install/04_rbac.yaml`) —
   Grants the operator permissions to manage Deployments, RBAC, CSRs, MCPs,
   KubeletConfigs, EC2NodeClasses, NodeClaims, Infrastructure, ClusterOperator
   status, and MachineSets (read-only, for NodeClass discovery).

2. **`karpenter`** (created dynamically by the deployment controller) — Grants
   the operand permissions for Karpenter's core operations: NodePools,
   NodeClaims, Nodes, Pods, EC2NodeClasses, and cloud-specific resources. This
   ClusterRole is assembled from a generic base plus cloud-specific rules from
   `CloudProvider.OperandConfig().RBACRules`.

---

## Data flow: provisioning a new node

```
0. Operator waits for credentials Secret → creates operand Deployment
   Init container (check-credentials) blocks until cloud API accepts credentials
1. User creates NodePool referencing EC2NodeClass "default"
2. Karpenter (operand) selects instance type, calls EC2 CreateFleet
3. EC2 instance boots RHCOS with Ignition from EC2NodeClass.spec.userData
4. Ignition fetches rendered MachineConfig from MCS at /config/karpenter-default
5. Kubelet starts with registerWithTaints: [karpenter.sh/unregistered:NoExecute]
6. Kubelet requests bootstrap CSR (kube-apiserver-client-kubelet signer)
7. karpenter-operator machine approver:
   a. Finds unassigned NodeClaims
   b. Calls EC2 DescribeInstances to get private DNS names
   c. Matches CSR node name to instance DNS → approves CSR
8. Kubelet obtains client cert, registers node with API server
9. Karpenter links NodeClaim to Node (via matching providerID)
10. Karpenter removes karpenter.sh/unregistered taint
11. Kubelet requests serving CSR (kubelet-serving signer)
12. Machine approver finds NodeClaim with matching nodeName → approves
13. Node is Ready, workloads schedule onto it
```
