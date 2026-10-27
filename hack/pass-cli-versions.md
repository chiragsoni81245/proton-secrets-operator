# Pinning pass-cli

The image build needs, per architecture, the download URL and SHA-256 of the
release being pinned. Both come from Proton's `versions.json`
(https://proton.me/download/pass-cli/versions.json). They are build args on
purpose: there is no default, so a build cannot pick an unverified binary.

`hack/pass-cli-pin.sh` reads that file and prints everything below, ready to
use. It only reads JSON (it does not download the binary). Review the output,
then either set the GitHub repository variables it prints (needed by
`release.yml`) or pass the printed `--build-arg`s to docker:

```bash
hack/pass-cli-pin.sh          # prints `gh variable set ...` lines and --build-arg flags
```

```bash
docker buildx build --target manager \
  --build-arg PASS_CLI_VERSION=<x.y.z> \
  --build-arg PASS_CLI_URL_AMD64=<url> --build-arg PASS_CLI_SHA256_AMD64=<sha256> \
  --build-arg PASS_CLI_URL_ARM64=<url> --build-arg PASS_CLI_SHA256_ARM64=<sha256> \
  -t ghcr.io/chiragsoni81245/proton-secrets-operator:<tag> .

# standalone image for the chart's passCli.image override
docker buildx build --target pass-cli-image ... -t ghcr.io/chiragsoni81245/pass-cli:<x.y.z> .
```

Compatibility: the operator logs/exports the running version
(`protonsecret_passcli_info`). The parsing assumptions live in
`internal/proton/cli.go`; re-verify them when bumping.
