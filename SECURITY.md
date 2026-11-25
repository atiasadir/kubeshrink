# Security policy

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report them privately through GitHub's private vulnerability reporting: open the repository's **Security** tab and choose **Report a vulnerability**. If that option is not available, send a private message to the maintainer, @atiasadir on GitHub, asking for a private channel.

Please include what you found, how to reproduce it, and the version (`kubeshrink version`). You can expect an acknowledgement within a week.

## Scope

KubeShrink is designed to be read-only. Anything that lets it modify a cluster, read Secrets or ConfigMap data, send cluster data off the machine, or write exports with broad permissions is a security bug.

## Supported versions

The project is pre-1.0; only the latest commit on `main` receives fixes.

## How vulnerabilities are handled

- **Dependencies.** Dependabot proposes weekly updates for Go modules, the GitHub Actions the workflows use (pinned to commit SHAs) and the container base images (pinned by digest).
- **Code.** CI runs `govulncheck` on every push and pull request and fails when a known vulnerability is reachable from KubeShrink's code.
- **Image.** CI builds the container image, scans it with Trivy and fails on HIGH or CRITICAL findings that have a fix; `make image scan` runs the same check locally. CI also keeps a CycloneDX SBOM of the image as a build artifact. The image is a static binary on Azure Linux's distroless minimal base (mcr.microsoft.com/azurelinux/distroless/minimal): no shell and no package manager, so little in it can be vulnerable. To run exactly the image that was scanned, install it by digest with the chart's `image.digest` value.
- **Schedule.** CI also runs every Monday, so a vulnerability published since the last push still fails CI.
- **Fixing.** A fixable HIGH or CRITICAL finding is fixed on `main` as soon as practical, usually by updating the dependency or the base image digest. Findings without a fix are tracked until one exists. Nothing is published yet; once releases exist, fixes ship as patch releases.
- **Releases.** Pushing a version tag (`v*`) runs `.github/workflows/release.yml`: the same scan gate as CI, then a multi-arch image pushed to `ghcr.io/atiasadir/kubeshrink`, signed by digest with cosign keyless signing (GitHub OIDC) and given its CycloneDX SBOM as a signed attestation. The README shows how to verify both.
