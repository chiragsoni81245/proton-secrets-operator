#!/usr/bin/env bash
# Regenerates charts/.../templates/crd.yaml from the controller-gen output.
set -euo pipefail
cd "$(dirname "$0")/.."
src=config/crd/bases/proton.chiragsoni.org_protonsecrets.yaml
out=charts/proton-secrets-operator/templates/crd.yaml
python3 - "$src" "$out" <<'PY'
import sys
src = open(sys.argv[1]).read()
needle = "metadata:\n  annotations:\n"
assert needle in src, "unexpected CRD layout"
src = src.replace(needle, needle + "    {{- if .Values.crds.keep }}\n    helm.sh/resource-policy: keep\n    {{- end }}\n", 1)
open(sys.argv[2], "w").write("{{- if .Values.crds.install }}\n" + src.rstrip() + "\n{{- end }}\n")
PY
