# Agent Instructions

Rules and gotchas for AI agents working on the `karpenter-operator` repository.

## What This Repo Is

`karpenter-operator` is an OpenShift operator that deploys and manages [Karpenter](https://karpenter.sh/) on OpenShift clusters. It discovers the cluster's cloud provider at runtime and configures Karpenter accordingly.

## Multi-cloud Rules

This operator must support multiple cloud providers. Only AWS is implemented so far, but these rules apply to all code changes:

- **Never import cloud-provider SDKs in generic packages.** Cloud-specific code belongs in `pkg/cloudprovider/<provider>/`. Generic code in `cmd/`, `pkg/operator/`, and `pkg/controllers/` must interact through the `CloudProvider` interface only.
- **Detect the provider at runtime** from the `Infrastructure` CR (`status.platformStatus.type`), not from build tags or hardcoded assumptions.
- When adding provider-specific behavior, ask: "What would the other providers need here?" and leave room for it (interface methods, switch statements, TODOs).

## Operator / Operand Separation

The **operator** (this binary) manages the **operand** (Karpenter). They are separate images in separate Deployments. Do not conflate them — the operator creates and manages the operand's Deployment, ServiceAccount, and RBAC. They share a namespace but have independent credentials and RBAC.

The operand image varies by cloud provider — each provider has its own Karpenter image. The operator must select the correct operand image based on the discovered infrastructure.

## Coding Gotchas

- **Dependencies are vendored.** Always run `make vendor` after changing `go.mod`. Do not use `go get` alone.
- **Import ordering** is enforced by `.golangci.yml`. Run `make lint` after changes — it auto-fixes.
- **Run `make verify`** after any code change. It runs vet, fmt, lint, and tests together.
- **No narrating comments.** Do not add comments that restate what the code does. Comments should only explain non-obvious intent, trade-offs, or constraints.
