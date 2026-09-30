# Cadence control plane: one binary with the API, the event stream and the embedded SPA.
# Build context is the repository root:
#   docker build -f control-plane/Dockerfile --build-arg CADENCE_VERSION=0.1.0 .

# ---- web: build the SPA when the web project is present; otherwise the committed placeholder page stays.
FROM node:22 AS web
WORKDIR /src/web
COPY web/ ./
RUN if [ -f package-lock.json ]; then \
      npm ci && npm run build; \
    else \
      echo "web/package-lock.json missing: keeping the placeholder UI" && mkdir -p dist; \
    fi

# ---- control plane
FROM golang:1.27 AS build
ARG CADENCE_VERSION=dev
WORKDIR /src/control-plane
COPY control-plane/go.mod control-plane/go.sum ./
RUN go mod download
COPY control-plane/ ./
COPY --from=web /src/web/dist/ internal/webui/dist/
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${CADENCE_VERSION}" -o /out/cadence ./cmd/cadence \
 && mkdir -p /out/data

# ---- runtime: slim Debian with git (project repositories, R10: merges, hooks and smart HTTP need the real git), the
# CA bundle (GitHub over HTTPS) and the PostgreSQL 17 client from the PGDG repository (pg_dump / pg_restore for
# backups and the restore test; their major version must match the compose file's postgres:17 — bookworm itself
# ships 15); non-root (uid 65532). The data volume must be writable by that uid.
FROM debian:bookworm-slim
ADD https://www.postgresql.org/media/keys/ACCC4CF8.asc /usr/share/keyrings/pgdg.asc
RUN chmod 0644 /usr/share/keyrings/pgdg.asc \
 && echo "deb [signed-by=/usr/share/keyrings/pgdg.asc] http://apt.postgresql.org/pub/repos/apt bookworm-pgdg main" \
      > /etc/apt/sources.list.d/pgdg.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates postgresql-client-17 \
 && rm -rf /var/lib/apt/lists/* /usr/share/doc /usr/share/man \
 && groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --no-create-home --home-dir /data --shell /usr/sbin/nologin nonroot
COPY --from=build /out/cadence /cadence
# An empty /data owned by the runtime uid, so a fresh named volume starts writable.
COPY --from=build --chown=65532:65532 /out/data /data
# Where the agent host's token is kept (CADENCE_HOST_TOKEN_FILE): a volume only the control plane writes.
COPY --from=build --chown=65532:65532 /out/data /host-credential
# Where the egress proxy's token is kept (CADENCE_EGRESS_TOKEN_FILE): written by the control plane, read by the proxy.
COPY --from=build --chown=65532:65532 /out/data /egress-credential
# Backup sets and the content-store mirror (CADENCE_BACKUP_DIR): a volume now, a mount in phase 4.
COPY --from=build --chown=65532:65532 /out/data /backups
ENV CADENCE_ADDR=0.0.0.0:8080 \
    CADENCE_DATA_DIR=/data
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/cadence"]
CMD ["serve"]
