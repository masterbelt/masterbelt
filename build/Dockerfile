# The masterbelt CLI on a minimal, non-root base. The binary is the pre-built
# static one from `make dist` (CGO off), copied per target architecture, so the
# image ships the exact published artifact rather than a separately-built one.
# Build context is build/; buildx fills TARGETOS/TARGETARCH per --platform.
FROM gcr.io/distroless/static:nonroot

ARG TARGETOS
ARG TARGETARCH
COPY ctx/${TARGETOS}/${TARGETARCH}/masterbelt /usr/local/bin/masterbelt

ENTRYPOINT ["/usr/local/bin/masterbelt"]
CMD ["--help"]
