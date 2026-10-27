#!/usr/bin/env bash
# Prints the pass-cli pin (version, URL and SHA-256 per Linux arch) from Proton's versions.json, as GitHub repository
# variables for release.yml and as docker build args. It only reads JSON; it does not download the binary.
# Review the output before using it: the pin is what the image build verifies the binary against.
#
# Usage: hack/pass-cli-pin.sh [versions-url]
#        hack/pass-cli-pin.sh | tee /dev/stderr | ...   (see the printed `gh variable set` commands)
set -euo pipefail
URL="${1:-https://proton.me/download/pass-cli/versions.json}"

curl -fsSL --proto '=https' --tlsv1.2 "$URL" | python3 -c '
import json, sys
d = json.load(sys.stdin)["passCliVersions"]
linux = d["urls"]["linux"]
rows = {
    "PASS_CLI_VERSION": d["version"],
    "PASS_CLI_URL_AMD64": linux["x86_64"]["url"],
    "PASS_CLI_SHA256_AMD64": linux["x86_64"]["hash"],
    "PASS_CLI_URL_ARM64": linux["aarch64"]["url"],
    "PASS_CLI_SHA256_ARM64": linux["aarch64"]["hash"],
}
for k, v in rows.items():
    assert v and all(c.isalnum() or c in ".-_:/" for c in v), f"unexpected value for {k}: {v!r}"
print("# GitHub repository variables (Settings > Secrets and variables > Actions > Variables):")
for k, v in rows.items():
    print(f"gh variable set {k} --body {v}")
print()
print("# or docker build args:")
print(" ".join(f"--build-arg {k}={v}" for k, v in rows.items()))
'
