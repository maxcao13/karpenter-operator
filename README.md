# karpenter-operator

OpenShift operator that deploys and manages [Karpenter](https://karpenter.sh/) on Red Hat OpenShift clusters. Managed by the Cluster Version Operator (CVO) as part of the OpenShift release payload.

## Building

```bash
make build        # Build the operator binary
make test         # Run unit tests
make lint         # Run golangci-lint
make verify       # Run all checks (vet, fmt, lint, test)
make docker-build # Build container image
```

## Deploying (dev)

```bash
make deploy \
  IMG=quay.io/you/karpenter-operator:dev \
  OPERAND_IMG=quay.io/you/karpenter:dev \
  CLUSTER_NAME=my-cluster \
  DEV=true
```

## Documentation

- [AGENTS.md](AGENTS.md) — design principles and coding conventions for contributors and AI agents
