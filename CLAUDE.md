# Karpenter Operator — Quick Reference

> See `AGENTS.md` for comprehensive details. Keep both files up to date when
> conventions change.

## What this is

CVO-managed ClusterOperator that deploys Karpenter on OpenShift (AWS).
Modeled after [cluster-autoscaler-operator](https://github.com/openshift/cluster-autoscaler-operator).

## Quick reference

```bash
make build          # Build binary
make test           # Unit tests
make verify         # Full check suite (vet + fmt + lint + test)
make vendor         # go mod tidy + vendor
make images         # Build container image
make deploy         # kubectl apply -f install/ (raw manifests)
make deploy-dev IMG=... OPERAND_IMG=... CLUSTER_NAME=... AWS_REGION=...
make undeploy       # kubectl delete -f install/
```

## Key rules

- CVO-managed: manifests in `install/`, copied to `/manifests` in image, `LABEL io.openshift.release.operator true`
- No OLM, no kustomize, no operator-sdk
- ClusterOperator manifest (`06_`) must come after Deployment (`05_`) alphabetically
- Namespace and CredentialsRequest are `00_` (applied first)
- StatusReporter updates `karpenter` ClusterOperator CR — report previous version during upgrades until all operands at new version
- All manifests need release annotations (`include.release.openshift.io/self-managed-high-availability`, etc.)
- Single-cluster model only — no Hypershift code in this repo
- CRD spec in `install/` comes from upstream — do not hand-edit
- Vendored deps — run `make vendor` after changes
- `replace` directives in go.mod point to OpenShift forks
- Flags for deployment/controller-runtime config; env vars for CVO-injected values

## CVO dev guide

- [ClusterOperator CR](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/clusteroperator.md)
- [Operator integration](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/operators.md)
- [Upgrades](https://github.com/openshift/enhancements/blob/master/dev-guide/cluster-version-operator/dev/upgrades.md)
