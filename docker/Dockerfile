# Minimal runtime image. The binary is cross-compiled on the host before
# `docker build` (avoids the go.mod replace-directive dance that breaks
# in-Docker source builds). This Dockerfile only stages the artifacts.
#
# Multi-arch: buildx sets TARGETARCH automatically. For local single-
# platform builds, pass --build-arg TARGETARCH=arm64 (or amd64). The
# Makefile docker-image target picks the host arch.
#
# Required artifacts before `docker build`:
#   bin/fslite-linux-${TARGETARCH}
#   bin/fslite-seed-linux-${TARGETARCH}
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETARCH=arm64

COPY bin/fslite-linux-${TARGETARCH}       /usr/local/bin/fslite
COPY bin/fslite-seed-linux-${TARGETARCH}  /usr/local/bin/fslite-seed

EXPOSE 8080
USER nonroot
ENTRYPOINT ["/usr/local/bin/fslite"]
