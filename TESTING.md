# Testing karpenter-operator

See [CONTRIBUTING.md](./CONTRIBUTING.md#testing) for test naming conventions and
review requirements.

## Running tests

Run these commands from the repository root:

| Command | Purpose |
| --- | --- |
| `make test` | Unit tests under `pkg/`, including HCP Deployment fixtures |
| `go test ./test/pkg/... -count=1` | Tests for shared test helpers, including the fixture helper |
| `make build` | Build the operator |
| `make verify` | Vet, lint, unit tests, generation, manifest checks, and working-tree cleanliness |
| `make e2e` | Cluster E2E suites; requires `KUBECONFIG` |

The `api/` directory is a separate Go module. Root-module commands do not
traverse it; run module-specific tests there when changing its code.

## Deployment fixture tests

Fixture tests compare a deterministically rendered resource with an expected
YAML manifest checked into `testdata/`. They catch unintended manifest changes
without starting a cluster.

The HCP Deployment fixture test lives in
[`pkg/controllers/karpenter/hcp_deployment_fixture_test.go`](./pkg/controllers/karpenter/hcp_deployment_fixture_test.go).
It:

1. Creates a fixed HostedControlPlane, controller configuration, and fake cloud
   configuration.
2. Runs the real HCP controller reconciliation, including construction of
   `operandConfig` and the Deployment.
3. Captures and serializes the Deployment server-side apply payload **before**
   the fake client applies it.
4. Compares that YAML with the checked-in fixture.
5. Reconciles again and compares with the same fixture to check that rendering
   stays stable on subsequent reconciliations.

The fixtures cover the full desired Deployment: images, command and arguments,
environment variables, volumes and mounts, probes, resources, security contexts,
labels, annotations, and owner references. The test includes both baseline and
changed-input cases.

Capturing the apply payload avoids API-generated fields such as resource
versions, managed fields, and status. Do not strip meaningful desired fields
just to make a fixture pass.

### Check for regressions

Run the focused tests without `UPDATE`:

```shell
go test ./pkg/controllers/karpenter -run '^TestHCPDeploymentFixture$' -count=1
```

Normal runs never create or rewrite fixture files. A missing fixture fails the
test; a changed manifest fails with a `-want +got` diff and the fixture path.
`-count=1` ensures the test executes rather than using a cached result.

Investigate the diff before deciding whether to change the implementation or
update the expected manifest.

### Update fixtures for an intentional change

```shell
UPDATE=true go test ./pkg/controllers/karpenter -run '^TestHCPDeploymentFixture$' -count=1
git diff -- pkg/controllers/karpenter/testdata/
```

Only the exact value `UPDATE=true` enables fixture creation or updates.
`UPDATE=false` does not. Scope updates to the affected tests rather than
regenerating unrelated fixtures.

Review every YAML change. In particular, check command and arguments, logging
configuration, credentials references, security settings, and owner references.
Never approve an unexpected diff merely by regenerating the fixture.

Rerun without `UPDATE` after reviewing:

```shell
go test ./pkg/controllers/karpenter -run '^TestHCPDeploymentFixture$' -count=1
make test
go test ./test/pkg/... -count=1
```

Commit fixture changes with the implementation changes that require them. Run
`make build` and `make verify` before submitting; the final verification
clean-tree check requires committed changes. `make verify` does not regenerate
these test fixtures.

### Add a new fixture case

Use table-driven subtests with names following
`When <condition>, it should <expected behavior>`. Choose fixed, realistic
inputs and exercise the actual rendering or reconciliation path rather than
duplicating production manifest construction in the test.

Use [`test/pkg/testutil.CompareWithFixture`](./test/pkg/testutil/fixtures.go)
to compare the desired output:

```go
testutil.CompareWithFixture(t, desiredDeployment)
```

The helper marshals objects as YAML. Strings and byte slices are compared
verbatim, which allows a test to capture serialized output before a client
mutates it.

Each test gets a file relative to its package:

```text
testdata/zz_fixture_<sanitized test name>.yaml
```

The full test name includes its subtest name. Unsupported filename characters
are replaced with underscores. Use distinct subtests for distinct expected
outputs; repeated comparisons in one subtest share a fixture and must produce
the same output.

Create a new fixture using the scoped `UPDATE=true` command, inspect it, and
commit it with the test. Renaming a test changes its fixture filename; remove
the old file if it is no longer used. Newly created files are untracked, so
check `git status` as well as `git diff` when reviewing them.

## Test boundaries

- **Fixture/unit tests:** Given fixed inputs, does the operator render the
  expected desired workload?
- **API integration/envtest:** Does the resource work with real API validation,
  defaulting, watches, and server-side apply semantics? The fake-client fixture
  test does not prove these properties.
- **Cluster E2E:** Does the deployed operator and operand actually start,
  reconcile, and work with the platform?

A passing fixture test does not prove operand startup, rollout readiness, cloud
authentication, or hosted-cluster lifecycle behavior. Keep behavioral unit
tests alongside fixture snapshots and use integration or E2E tests where those
boundaries matter.

This workflow follows HyperShift's
[`CompareWithFixture` pattern](https://github.com/openshift/hypershift/blob/b7503e957a58cbce6847a1640f9341ca0a145f53/support/testutil/testutil.go#L27).
