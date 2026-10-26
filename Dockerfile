# TARGETOS/TARGETARCH are set by buildx; with the legacy builder they are
# empty and Go builds for the native platform.
FROM golang:1.23 AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/vpowerd ./cmd/vpowerd
# Empty state dir, owned by the runtime user so a fresh volume inherits it.
RUN mkdir -p /out/state

# Static binary only: no shell, no package manager, runs as uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vpowerd /vpowerd
COPY --from=build --chown=65532:65532 /out/state /var/lib/vpowerd
VOLUME /var/lib/vpowerd
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/vpowerd", "-healthcheck"]
ENTRYPOINT ["/vpowerd"]
