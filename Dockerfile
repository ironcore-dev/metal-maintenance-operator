# Build the manager binary
FROM --platform=$BUILDPLATFORM golang:1.26.3 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY cmd/metal-sanitizer/ cmd/metal-sanitizer/
COPY api/ api/
COPY internal/ internal/

# Build
# the GOARCH has not a default value to allow the binary be built according to the host where the command
# was called. For example, if we call make docker-build in a local env which has the Apple Silicon M1 SO
# the docker BUILDPLATFORM arg will be linux/arm64 when for Apple x86 it will be linux/amd64. Therefore,
# by leaving it empty we can ensure that the container and binary shipped on it will have the same platform.
FROM builder AS manager-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o manager cmd/main.go

FROM builder AS metal-sanitizer-builder
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o metal-sanitizer ./cmd/metal-sanitizer

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot AS manager
WORKDIR /
COPY --from=manager-builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]

# Runtime stage for metal-sanitizer: Debian-slim is required for util-linux (wipefs, blockdev, blkdiscard)
# and coreutils (dd). These tools are invoked by metal-sanitizer at runtime to wipe block devices.
FROM debian:testing-slim AS metal-sanitizer
RUN apt-get update && apt-get install -y --no-install-recommends \
        util-linux \
        coreutils \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=metal-sanitizer-builder /workspace/metal-sanitizer /usr/local/bin/metal-sanitizer
ENTRYPOINT ["/usr/local/bin/metal-sanitizer"]
