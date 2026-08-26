# Builds the image from source, for `make docker` and local work. The release
# image is built by GoReleaser from Dockerfile.release, which copies an
# already-compiled binary instead. Keep the two in step.
#
# Build with the latest stable Go release.
FROM golang:1.27.0-alpine AS build

ARG VERSION=dev
WORKDIR /src

# Dependencies resolve in their own layer so source edits do not re-download.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/stufently/zabbix-ai-cli-mcp/internal/cli.Version=${VERSION}" \
    -o /out/zabbix-ai-cli-mcp ./cmd/zabbix-ai-cli-mcp

# distroless has no shell, so the state directory has to be built here and
# copied in already owned by the unprivileged user the image runs as.
RUN mkdir -p /out/state && chmod 700 /out/state

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/zabbix-ai-cli-mcp /usr/local/bin/zabbix-ai-cli-mcp

# Plans and the audit log live here. Mount a writable volume over it to keep
# them; the root filesystem is expected to be read-only.
COPY --from=build --chown=65532:65532 /out/state /var/lib/zabbix-ai-cli-mcp

ENV ZABBIX_AI_CLI_MCP_STATE_DIR=/var/lib/zabbix-ai-cli-mcp
VOLUME /var/lib/zabbix-ai-cli-mcp

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/zabbix-ai-cli-mcp"]
CMD ["mcp"]
