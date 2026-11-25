# SPDX-License-Identifier: Apache-2.0
# KubeShrink image for the in-cluster proof of concept. Build it locally with
# `make image`. No image is published yet.
#
# Only go.mod, go.sum, cmd/ and internal/ reach the build (see .dockerignore),
# so local real-cluster output can never end up in an image.

ARG GO_VERSION=1.26

FROM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /out/kubeshrink ./cmd/kubeshrink

# Static, distroless and non-root: no shell and no package manager.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kubeshrink /kubeshrink
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/kubeshrink"]
CMD ["serve"]
