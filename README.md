# proton-secrets-operator

A Kubernetes operator that syncs secrets from [Proton Pass](https://proton.me/pass) into native
`Secret` objects. You declare a `ProtonSecret`; the operator resolves its `pass://` references with
[`pass-cli`](https://protonpass.github.io/pass-cli/) and creates/updates the Secret.

> Unofficial community project. Not affiliated with or endorsed by Proton AG.

```yaml
apiVersion: proton.chiragsoni.org/v1alpha1
kind: ProtonSecret
metadata: {name: grafana-admin, namespace: monitoring}
spec:
  refreshInterval: 1h
  data:
    - {secretKey: admin-user,     remoteRef: pass://homelab/grafana/username}
    - {secretKey: admin-password, remoteRef: pass://homelab/grafana/password}
  rolloutTargets: [{kind: Deployment, name: grafana}]
```

## How it works

```
 ProtonSecret (CR) ──watch──▶ controller ──resolve pass://…──▶ pass-cli ──▶ Proton Pass
                                  │
                                  ├──server-side apply──▶ Secret (owned by the CR)
                                  └──optional──────────▶ restart Deployments/StatefulSets/DaemonSets
```

A sync is triggered when:
- the `ProtonSecret` spec changes, or `proton.chiragsoni.org/force-sync` is set to a new value;
- `refreshInterval` elapses (default 1h, minimum 1m, ±10% jitter);
- someone edits or deletes the target Secret (it is reverted).

Behaviour worth knowing:
- If Proton is unreachable or the PAT is invalid, existing Secrets are **left untouched**; the
  `ProtonSecret` goes `Ready=False` and retries with backoff.
- A bad reference or a name clash with a foreign Secret is treated as terminal: no hot loop, retry
  about every 10 minutes at most.
- The operator never adopts a Secret it did not create unless `target.creationPolicy: Merge`.
- Writes are skipped when the content hash matches, so unchanged secrets cause no API writes or restarts.
- Only Secrets labelled `app.kubernetes.io/managed-by=proton-secrets-operator` are cached in memory.
- It needs cluster-wide Secret write access (the CRD is namespaced). Anyone who can create a
  `ProtonSecret` can read any vault the PAT can see, so scope the PAT to one dedicated vault and
  restrict who may create `ProtonSecret`s.

## Repository layout

The code is split in three layers (API, reconciliation logic, Proton access) plus the things that
build, package and deploy it. Dependencies only point downwards:
`cmd → controller → proton / metrics → api`.

```
.
├── api/v1alpha1/               THE API: what users can write
│   ├── protonsecret_types.go     ProtonSecret spec/status, validation markers, constants
│   ├── groupversion_info.go      group proton.chiragsoni.org, version v1alpha1
│   └── zz_generated.deepcopy.go  generated, do not edit (make generate)
│
├── cmd/main.go                 Entry point: flags, manager setup, wiring (cache, probes, metrics)
│
├── internal/                   THE LOGIC (not importable from outside)
│   ├── controller/               Reconcile loop: finalizer, resolve, apply Secret, rollout, status
│   │   ├── protonsecret_controller.go
│   │   └── reconcile_unit_test.go   fake-client tests (no cluster needed)
│   ├── proton/                   Everything that talks to Proton Pass
│   │   ├── client.go               Client interface + error classes (the seam for testing)
│   │   ├── cli.go                  pass-cli implementation: login, session, timeouts
│   │   └── fake.go                 in-memory Client for tests
│   └── metrics/                  Prometheus metrics (no secret values in labels)
│
├── charts/proton-secrets-operator/   HOW YOU INSTALL IT (Helm chart, the supported install path)
│   ├── values.yaml, values.schema.json
│   └── templates/                deployment, rbac, crd, service, pdb, networkpolicy, monitoring
│
├── config/                     Kubebuilder/controller-gen output + scaffolding
│   ├── crd/bases/                generated CRD (source for the chart's crd.yaml)
│   ├── rbac/role.yaml            generated from // +kubebuilder:rbac markers
│   ├── samples/                  example ProtonSecret
│   └── default, manager, …       kustomize install (kubebuilder default; not the primary path)
│
├── Dockerfile                  Targets: manager (operator, default) · pass-cli-image (CLI only)
├── hack/                       Helper scripts and notes
│   ├── sync-chart-crd.sh         config/crd → chart template (make chart-crd)
│   ├── pass-cli-versions.md      how to pin pass-cli (version, URL, SHA-256)
│   └── boilerplate.go.txt        license header for generated code
├── docs/TESTING.md             Staged test plan (pass-cli spike → local → kind → homelab)
├── test/                       Kubebuilder e2e scaffold (not yet filled in)
├── .github/workflows/          lint, test, chart (lint + CRD sync check), release (images + chart)
├── Makefile, PROJECT, go.mod   Build tooling and Kubebuilder metadata
└── AGENTS.md                   Notes for AI coding agents (Kubebuilder default)
```

### Why it is split this way
- **`api/` is separate from the logic** so the CRD schema can be reviewed and versioned on its own;
  changing it means running `make manifests generate chart-crd`.
- **`internal/proton` hides `pass-cli` behind an interface.** `pass-cli` is the only supported way
  in, so it is treated as an unreliable external program (timeouts, no shell, token via env only).
  The controller never imports `os/exec`; tests use `proton.Fake`.
- **`internal/controller` holds all Kubernetes behaviour** (ownership, hashing, server-side apply,
  finalizers, rollout). It has no Proton-specific code.
- **The chart is the install path; `config/` is generator output.** `config/crd/bases` and
  `config/rbac/role.yaml` come from the Go markers. The chart's `crd.yaml` is derived from the former
  (CI fails if it drifts), but the chart's ClusterRole in `templates/rbac.yaml` is currently written by
  hand: keep it in sync with the `+kubebuilder:rbac` markers in the controller.
- **The Dockerfile has two final targets** so the `pass-cli` version can change independently: the
  chart's `passCli.image.*` values make an initContainer copy the binary from a separate image.

### Generated files: do not edit by hand
`api/**/zz_generated.deepcopy.go`, `config/crd/bases/*`, `config/rbac/role.yaml`,
`charts/**/templates/crd.yaml`. Regenerate with `make manifests generate chart-crd`.

## Install

1. Create a **read-only (viewer)** personal access token scoped to a dedicated vault
   (`pass-cli pat create`). PATs always expire: note the date.
2. `kubectl -n proton-secrets-system create secret generic proton-pat --from-file=token=<(printf %s "$TOKEN")`
3. `helm install proton-secrets-operator oci://ghcr.io/chiragsoni81245/charts/proton-secrets-operator -n proton-secrets-system --create-namespace --set pat.expiresAt=<RFC3339>`

`pat.expiresAt` feeds the `ProtonPATExpiringSoon` alert; the operator cannot read the expiry from Proton.

### Changing the pass-cli version
Set `passCli.image.repository` and `tag` (or `digest`) in the chart values. An initContainer copies
the binary from that image, so the CLI can be bumped without a new operator image. To build pinned,
checksum-verified images see [`hack/pass-cli-versions.md`](hack/pass-cli-versions.md).

## Development

| Command | Does |
|---------|------|
| `make test` | unit tests |
| `make manifests generate` | regenerate CRD, RBAC, deepcopy from the Go types and markers |
| `make chart-crd` | copy the generated CRD into the Helm chart |
| `make chart-lint` | `helm lint` + render the chart (needs helm) |
| `make install` / `make run` | install the CRD / run the operator locally against your kubeconfig |

Testing against real Proton Pass: [`docs/TESTING.md`](docs/TESTING.md).
