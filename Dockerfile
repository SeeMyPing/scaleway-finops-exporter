# syntax=docker/dockerfile:1

# Build stage: runs on the build platform and cross-compiles for the target,
# which is much faster than emulating the target platform with QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build

# Never download another toolchain: the image must match go.mod.
ENV GOTOOLCHAIN=local CGO_ENABLED=0

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG BRANCH=unknown
ARG BUILD_DATE=unknown

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags "-s -w \
        -X github.com/prometheus/common/version.Version=${VERSION} \
        -X github.com/prometheus/common/version.Revision=${REVISION} \
        -X github.com/prometheus/common/version.Branch=${BRANCH} \
        -X github.com/prometheus/common/version.BuildUser=docker \
        -X github.com/prometheus/common/version.BuildDate=${BUILD_DATE}" \
      -o /out/scaleway-finops-exporter ./cmd/scaleway-finops-exporter

# Runtime stage: static distroless image with CA certificates, running as the
# unprivileged "nonroot" user (65532). No shell, no package manager.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3

ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="scaleway-finops-exporter" \
      org.opencontainers.image.description="Prometheus exporter for Scaleway billing (FinOps) and environmental footprint (GreenOps) data" \
      org.opencontainers.image.source="https://github.com/SeeMyPing/scaleway-finops-exporter" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"

COPY --from=build /out/scaleway-finops-exporter /usr/local/bin/scaleway-finops-exporter

USER 65532:65532
EXPOSE 10056
ENTRYPOINT ["/usr/local/bin/scaleway-finops-exporter"]
