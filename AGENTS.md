# Karpenter Operator — Agent Guide

> **Keep this file up to date.** Whenever a new paradigm, coding convention, or
> architectural decision is introduced in this repo, update this file to reflect
> it. This is the single source of truth for agents working on this codebase.

## What this repo is

A CVO-managed ClusterOperator that deploys and manages [Karpenter](https://karpenter.sh/) on OpenShift clusters (AWS). It follows the same patterns as [cluster-autoscaler-operator](https://github.com/openshift/cluster-autoscaler-operator): manifests in `install/` copied to `/manifests` in the container image, a `StatusReporter` that updates a `ClusterOperator` CR, and the `LABEL io.openshift.release.operator true` marker.

This operator is designed standalone-first for self-managed OpenShift. Hypershift/managed-cluster integration is handled externally via a sidecar adapter pattern — this repo has no Hypershift-specific code.

## Repository layout

```
cmd/                        Entry point (standard flag package)
pkg/operator/               Manager setup, options, Run(), StatusReporter
pkg/controllers/
  deployment/               Reconciles karpenter core Deployment, SA, Role, RoleBinding (Go structs, no YAML templates)
  machineapprover/          Auto-approves CSRs for Karpenter-provisioned nodes
pkg/util/                   CSR parsing helpers
pkg/version/                Build-time version injection
install/                    CVO manifests (copied to /manifests in the container image)
  00_namespace.yaml         Namespace (must exist before any namespaced resource)
  00_credentials-request.yaml  AWS CredentialsRequest for CCO (must exist before Deployment)
  01-03_*.crd.yaml          Upstream Karpenter CRDs with release annotations
  04_rbac.yaml              SA, ClusterRole, ClusterRoleBinding
  05_deployment.yaml        Operator Deployment
  06_clusteroperator.yaml   ClusterOperator CR (must come after Deployment alphabetically)
  image-references          CVO image resolution
```

## Key design decisions

- **CVO-managed ClusterOperator**: this operator is part of the OpenShift release payload, not an OLM-managed layered operator. The CVO applies manifests from `/manifests` and watches the `ClusterOperator` CR for upgrade completion.
- **Single-cluster model** is first-class: the operator runs on and manages the same cluster. However, the design should remain amenable to guest-cluster patterns (e.g. Hypershift) as a future goal — avoid assumptions that would make that harder.
- **CRDs are applied by the CVO** from the `install/` directory, not programmatically at runtime by the operator.
- **Karpenter core deployment** (the actual `karpenter` pods) is managed by the `deployment` reconciler using Go structs (following cluster-autoscaler-operator pattern — no embedded YAML templates).
- **Machine approver** validates CSRs by cross-referencing NodeClaims with EC2 instance DNS names via the AWS API.
- **StatusReporter** periodically checks operand health and reports Available/Progressing/Degraded/Upgradeable conditions on the `karpenter` ClusterOperator CR.
- **Go module uses `replace` directives** pointing to OpenShift forks of `karpenter-provider-aws` and `karpenter` core.

## Build and development

```bash
make build          # Build binary to bin/karpenter-operator
make test           # Run unit tests (fmt + vet first)
make vet            # Go vet
make fmt            # Go fmt
make lint           # golangci-lint
make vendor         # go mod tidy + vendor
make verify         # vet + fmt + lint + test
```

Always run `make verify` before submitting a PR.

## Deployment

```bash
make deploy         # kubectl apply --server-side -f install/ (raw manifests, CVO-style)
make deploy-dev \   # Patches image/env vars into a temp copy, then applies
  IMG=quay.io/you/karpenter-operator:dev \
  OPERAND_IMG=quay.io/you/karpenter:dev \
  CLUSTER_NAME=my-cluster \
  AWS_REGION=us-east-1
make undeploy       # kubectl delete -f install/
```

`deploy` applies the manifests as-is (with placeholder images). Use `deploy-dev` during development to inject your own image and cluster-specific values without modifying checked-in files.

In production, the CVO applies the manifests from `/manifests` inside the container image automatically. The placeholder images in `install/` are resolved by the CVO via `image-references`.

## Container image

```bash
make images         # Build image (podman or docker auto-detected)
make push           # Push image
```

Two Dockerfiles:
- `Dockerfile` — CI (origin base images)
- `Dockerfile.rhel` — ART/downstream (RHEL 9 base images)

Both copy `install/` to `/manifests` and set `LABEL io.openshift.release.operator true`.

## Configuration

| Parameter | Source | Why |
|---|---|---|
| `--namespace` | CLI flag | Deployment config; downward API `$(NAMESPACE)` in manifest |
| `RELEASE_VERSION` | Env var only | Injected by CVO; used for ClusterOperator version reporting |
| `KARPENTER_IMAGE` | Env var only | Injected by CVO (image-references) |
| `AWS_REGION` | Env var only | Injected by deployment manifest |
| `CLUSTER_NAME` | Env var only | Injected by deployment manifest |
| `--metrics-bind-address` | CLI flag (default `:8080`) | controller-runtime convention |
| `--health-probe-bind-address` | CLI flag (default `:8081`) | controller-runtime convention |
| `--leader-elect` | CLI flag (default `false`) | controller-runtime convention |

The operator exits immediately if any required parameter is missing.

## Code conventions

### Go

- Go version: 1.25 (see `go.mod`)
- Vendored dependencies (`vendor/` directory). Do not modify `vendor/` directly; use `make vendor`.
- Run `make fmt` and `make vet` before committing. `make verify` runs the full suite.
- Prefer returning `error` over panicking. Wrap errors with `fmt.Errorf("context: %w", err)`.
- Accept interfaces, return concrete types.
- Use `context.Context` as the first parameter for functions that do I/O or call the API server.

### Testing

- Prefer Gherkin-style test names: `"When <condition>, it should <expected behavior>"`.
- Use `gomega` for test assertions.
- Write unit tests for new exported functions and reconciler logic.
- Tests live alongside source files (`*_test.go` in the same package).

### Commit messages

Use conventional commit format:

```
<type>(<scope>): <short summary>

<optional body>

Signed-off-by: Name <email>
```

Common types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`.

### Kubernetes resources in Go

- Build Kubernetes resources (Deployment, SA, Role, RoleBinding) as Go structs in reconciler code — do not use embedded YAML templates.
- Use `controllerutil.CreateOrUpdate` for idempotent reconciliation.
- Follow standard controller-runtime patterns: reconcile loops with proper error handling and requeuing.
- Use `sigs.k8s.io/controller-runtime/pkg/log` for structured logging.

### Manifests

- CVO manifests live in `install/` (flat numbered YAMLs, no kustomize).
- All manifests require release annotations (`exclude.release.openshift.io/internal-openshift-hosted`, `include.release.openshift.io/self-managed-high-availability`).
- CRD YAMLs in `install/` are upstream Karpenter CRDs with added release annotations — do not hand-edit the spec.
- The `07_clusteroperator.yaml` manifest **must** come alphabetically after `06_deployment.yaml`, or the CVO will block waiting for status that can never be reported.
- Binary name: `karpenter-operator`, installed to `/usr/bin/karpenter-operator` in the image.
- Version injected via `-ldflags -X .../pkg/version.Raw=<version>`.

## ClusterOperator status reporting

The `StatusReporter` (`pkg/operator/status.go`) runs as a `manager.Runnable` and periodically updates the `karpenter` ClusterOperator CR:

- **Available=True**: operand Deployment is ready with expected replicas.
- **Progressing=True**: operand is rolling out or not yet ready.
- **Degraded=True**: only after N consecutive check failures (threshold prevents flapping).
- **Upgradeable=True**: always true (for now).
- **Version reporting**: `status.versions[name=operator].version` must match `RELEASE_VERSION`. During upgrades, report the **previous** version until all operands are fully rolled out.

Condition message guidelines (from CVO dev guide):
- `Progressing`: terse, 5-10 words (shown as CLI column).
- `Degraded`: verbose, engineer-level detail for triage.
- `Available`: single sentence, no punctuation.
- `Degraded` must not be set during normal upgrades.

## CVO dev guide references

Consult these when making changes to manifests, status reporting, or upgrade behavior:

- [ClusterOperator Custom Resource](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/clusteroperator.md) — ClusterOperator CR contract: version reporting, condition semantics, related objects, version-reporting-during-upgrade protocol.
- [Operator integration with CVO](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/operators.md) — What goes in `/manifests`, manifest naming conventions, `image-references` for CVO image resolution, `LABEL io.openshift.release.operator=true`, release annotation requirements.
- [Upgrades and order](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/upgrades.md) — Runlevel assignments, upgrade ordering guarantees, N-1 minor version compatibility requirement.
- [Object deletion](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/object-deletion.md) — `release.openshift.io/delete: "true"` annotation for removing managed objects during upgrades.
- [CVO metrics](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/metrics.md) — `cluster_operator_conditions` and `cluster_operator_up` metrics derived from ClusterOperator status.
- [ClusterVersion CR](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/clusterversion.md) — Setting objects unmanaged for local development/testing.

## Common gotchas

- **Vendoring**: After adding/changing dependencies, always run `make vendor`. Building with `GOPROXY=off` in the Dockerfile means unvendored deps will fail the image build.
- **CRD YAMLs are upstream copies**: Do not edit the spec in `install/01-03_*.crd.yaml`. Update them by copying from the upstream provider repo and adding release annotations.
- **Namespace in ClusterRoleBinding subjects**: CVO does not transform cluster-scoped resources. If you change the target namespace, you must also update `install/04_rbac.yaml` subjects manually.
- **`replace` directives in go.mod**: This repo uses `replace` directives to point at OpenShift forks. When bumping dependencies, ensure the replacements stay consistent.
- **ClusterOperator ordering**: The CVO applies manifests alphabetically. The ClusterOperator CR (`06_`) must come after the Deployment (`05_`), or the CVO will block.
- **Namespace and CredentialsRequest come first** (`00_`): the namespace must exist before any namespaced resource, and the CredentialsRequest must exist before the Deployment so CCO can provision the secret.

## What NOT to do

- Do not add Hypershift-specific code to this repo. Hypershift integration runs as a separate sidecar (`karpenter-adapter`) built from the Hypershift repo.
- Do not add kustomize or OLM bundle machinery. This is a CVO-managed ClusterOperator.
- Do not add `--target-kubeconfig` or dual-cluster patterns. This is a single-cluster operator.
- Do not hand-edit CRD spec in `install/`. They come from upstream.
- Do not add hybrid flag-with-env-fallback for the same parameter. Flags are for deployment config and controller-runtime plumbing; env vars are for operand image, cloud config, and release version injected by CVO.
- Do not modify `vendor/` directly. Use `make vendor`.
