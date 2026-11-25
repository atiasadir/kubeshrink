# SPDX-License-Identifier: Apache-2.0
# KubeShrink image for the in-cluster proof of concept. Build it locally with
# `make image` and scan it with `make scan`. No image is published yet.
#
# Only go.mod, go.sum, cmd/ and internal/ reach the build (see .dockerignore),
# so local real-cluster output can never end up in an image.
#
# Both base images come from MCR and are pinned by digest; Dependabot proposes
# new tags and digests every week.

# Go toolchain image from MCR. Its default system-crypto backend needs cgo and
# OpenSSL at run time. MS_GO_NOSYSTEMCRYPTO=1 switches it off, so the binary
# uses Go's own crypto, is fully static and needs nothing from the base image.
FROM mcr.microsoft.com/oss/go/microsoft/golang:1.26.11-azurelinux3.0@sha256:55356fde1973c37a295fe7ba399138b96ade0500b25e1f07fbc41cd54ebb24c3 AS build
ENV CGO_ENABLED=0 MS_GO_NOSYSTEMCRYPTO=1
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/kubeshrink ./cmd/kubeshrink

# Azure Linux distroless minimal: patched often, with no shell and no package
# manager. The binary runs as an unprivileged user.
FROM mcr.microsoft.com/azurelinux/distroless/minimal:3.0.20261116@sha256:acf5853dc3651b91bdbf3aee1e5383e689452f6637bd9bf74c9537771cafa6c2
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="kubeshrink" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
COPY --from=build /out/kubeshrink /kubeshrink
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/kubeshrink"]
CMD ["serve"]
