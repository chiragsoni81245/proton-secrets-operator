# Testing guide

Work through the stages in order. Each one removes a class of risk before the next. Stages 1 and 2 are the
important ones: nothing has been run against real Proton Pass yet.

## Stage 0: unit tests (no tools needed)
```bash
make test            # or: go test ./internal/...
```
Covers create, update on Proton change, drift revert, Proton down (Secret untouched), not-found (no hot loop),
foreign Secret refused, delete. Expect green.

## Stage 1: `pass-cli` spike (the biggest unknown)
Goal: confirm the assumptions in `internal/proton/cli.go` against the real binary.

Setup (on your workstation, not in the cluster):
1. Install `pass-cli` (https://protonpass.github.io/pass-cli/), `pass-cli login`.
2. In Proton Pass create vault `homelab-test` with item `dummy` (Login type) containing a password.
   Add a second item `multi` with a multi-line note/custom field, and a value containing `$`, quotes, spaces, unicode.
3. Create a **viewer** PAT scoped to `homelab-test` with a short expiry; keep it out of shell history:
   `read -rs PROTON_PASS_PERSONAL_ACCESS_TOKEN; export PROTON_PASS_PERSONAL_ACCESS_TOKEN`
4. Use a clean session so you test what the pod will do:
   ```bash
   export PROTON_PASS_KEY_PROVIDER=fs PROTON_PASS_SESSION_DIR=$(mktemp -d)
   pass-cli logout --force 2>/dev/null; pass-cli login && pass-cli info
   ```

Record the exact output of each (paste back to me; redact values) and compare to the assumption:

| # | Command | Assumption in code | What to check |
|---|---------|--------------------|---------------|
| 1 | `pass-cli --version` | prints a version on the first line | format |
| 2 | `pass-cli item view "pass://homelab-test/dummy/password"` | stdout is **only** the value + one `\n` | extra labels/colours/quotes? |
| 3 | same for the `multi` item and special-character values | value is byte-exact (multi-line kept) | trailing newline trimming, escaping |
| 4 | `.../dummy/doesnotexist` (bad field), `.../nope/password` (bad item), `pass://nope/dummy/password` (bad vault) | non-zero exit; stderr contains "not found" / "no such" / "does not exist" | **exact stderr text and exit codes** |
| 5 | wrong token: `PROTON_PASS_PERSONAL_ACCESS_TOKEN=bad pass-cli login` | non-zero exit; stderr mentions token/unauthorized | stderr text |
| 6 | `pass-cli logout` then run #2 | fails with a "not logged in"/"session" style message | stderr text (drives the re-login path) |
| 7 | item in a vault the PAT is **not** scoped to | looks like not-found | not-found vs permission error |
| 8 | `pass-cli item view --output json "pass://..."` and without `--field` | not used today | JSON shape, in case we switch |
| 9 | two parallel `item view` calls | safe | any session-lock errors |
| 10 | Does stdout/stderr ever contain the token or value on failure? | never | security |

If any row differs, `classify()` and `Resolve()` in `internal/proton/cli.go` are what I change; send me the outputs.
Then I add golden-file tests from your real output.

## Stage 2: operator on your machine against a real cluster
Uses the real `pass-cli` and the homelab (or a kind cluster) API, without building images.
```bash
make install                                  # installs the CRD
printf %s "$PROTON_PASS_PERSONAL_ACCESS_TOKEN" > /tmp/pat && chmod 600 /tmp/pat   # delete afterwards
go run ./cmd --pat-file=/tmp/pat --session-dir=$(mktemp -d) --pass-cli-path=$(which pass-cli) \
   --metrics-bind-address=:8443 --metrics-secure=false
kubectl create ns pso-test
kubectl apply -n pso-test -f config/samples/proton_v1alpha1_protonsecret.yaml   # edit refs to homelab-test/dummy first
```
Run these scenarios and tick them off:

- [ ] **Create**: Secret appears, keys correct, `kubectl get psec -n pso-test` shows Ready=True, owner reference is the ProtonSecret, label `app.kubernetes.io/managed-by` set.
- [ ] **Value changes in Proton**: edit the password in Proton; after `kubectl annotate psec <n> proton.chiragsoni.org/force-sync=$(date +%s) --overwrite` the Secret updates within seconds.
- [ ] **Refresh interval**: set `refreshInterval: 1m`, change the value in Proton, wait; Secret follows without annotation.
- [ ] **Spec change**: add/remove a key; Secret gains/loses it (removed keys disappear).
- [ ] **Drift**: `kubectl edit secret` / `kubectl delete secret`; operator restores it.
- [ ] **Foreign Secret**: pre-create a Secret with the same name; ProtonSecret reports `SecretConflict`, existing data untouched. With `creationPolicy: Merge` it adopts.
- [ ] **Bad reference**: nonexistent item; Ready=False with reason, no log flooding, retries about every 10 min, not a hot loop.
- [ ] **Proton unavailable**: disable network or revoke the PAT mid-run; Secrets unchanged, Ready=False, exponential backoff; restore and it recovers on its own.
- [ ] **Expired session**: leave running > 2h (or `pass-cli logout` the session dir manually); next sync re-logs in transparently.
- [ ] **Rollout**: set `rolloutTargets` to a test Deployment, change the value; pod template annotation `proton.chiragsoni.org/restartedAt` is bumped and pods restart. Unchanged value: no restart.
- [ ] **Suspend**: `spec.suspend: true`; changes are ignored until unsuspended.
- [ ] **Delete**: `deletionPolicy: Delete` removes the Secret; `Retain` keeps it (without owner reference).
- [ ] **Leak check**: `kubectl get psec -o yaml`, `kubectl describe psec`, operator logs and events contain **no values or token**.
- [ ] **Metrics**: `curl localhost:8443/metrics | grep protonsecret_`. With `--pat-expiry-file=<file with an RFC3339 time>`, `protonsecret_pat_expiry_timestamp_seconds` shows it; edit the file and it changes within a minute.

## Stage 3: packaging
```bash
make chart-crd && git diff --stat charts/      # CRD in the chart matches the API types (expect no diff)
make chart-lint                                # needs helm
helm template t charts/proton-secrets-operator --set passCli.image.repository=x --set passCli.image.tag=1 | kubeconform -strict -ignore-missing-schemas
```
Build the images (needs docker; see `hack/pass-cli-versions.md` for the build args) and run:
```bash
docker run --rm --read-only <image> --help                     # starts as non-root with a read-only rootfs
docker run --rm --entrypoint pass-cli <image> --version        # binary runs on this base image (glibc/deps)
```
`pass-cli --version` failing here means the base image is missing a dependency: tell me the error.

## Stage 4: in-cluster (kind first, then homelab)
```bash
kind create cluster
kubectl create ns proton-secrets-system
kubectl -n proton-secrets-system create secret generic proton-pat --from-literal=token="$PROTON_PASS_PERSONAL_ACCESS_TOKEN" --from-literal=expires-at=2026-12-31T23:59:59Z
kind load docker-image <image>
helm install pso charts/proton-secrets-operator -n proton-secrets-system --set image.repository=<img> --set image.tag=<tag> --set image.pullPolicy=Never
```
Repeat the Stage 2 checklist, plus:
- [ ] Pod runs under PSA `restricted` (`kubectl label ns proton-secrets-system pod-security.kubernetes.io/enforce=restricted`).
- [ ] Both replicas up, one holds the lease; `kubectl delete pod` the leader, the other takes over and syncing continues.
- [ ] Readiness fails (pod NotReady) when the PAT is wrong; recovers after fixing the Secret and `rollout restart`.
- [ ] With `passCli.image.*` set, the initContainer copies the binary and `protonsecret_passcli_info` shows that version. Change the tag, roll out, version changes.
- [ ] Secret cache is limited: operator memory stays flat as you add unrelated Secrets (`kubectl top pod`).
- [ ] `helm upgrade` with a changed CRD applies it; `helm uninstall` keeps the CRD (`crds.keep`).
- [ ] NetworkPolicy on (`networkPolicy.enabled=true`): still syncs (check apiserver and Proton reachability on your CNI).

## Stage 5: homelab rollout (GitOps)
The Argo apps `proton-secrets-operator` (wave 3) and `proton-secrets` (wave 4) are already in `k8s-homelab`. Prerequisites:
1. Operator repo pushed to GitHub and (if private) added to Argo as a repository credential.
2. Image published: tag a release, which runs `release.yml`. Set repo variables first (`PASS_CLI_VERSION`, `PASS_CLI_URL_AMD64`, `PASS_CLI_SHA256_AMD64` and ARM64 pair).
3. `sudo tools/create-proton-pat-secret.sh` (asks for the token and its expiry; both go in `proton-pat`).
4. Create vault `homelab`, item `dummy`, field `password` in Proton Pass.

Then verify: `kubectl -n argocd get app proton-secrets-operator proton-secrets` Healthy/Synced,
`kubectl -n proton-secrets-system get protonsecret,secret example`, the metrics Service answers (scraping is configured on the homelab side), and the
`ProtonPAT*` rules are loaded in vmalert. Only after that migrate real secrets one at a time (Grafana first, with `rolloutTargets`).

## When something fails
Send me: the failing step number, `kubectl describe protonsecret <n>`, operator logs (they are value-free), and the
raw `pass-cli` output for that case (redact values).
