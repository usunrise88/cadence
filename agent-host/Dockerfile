# Cadence agent host: Claude Code (claude-agent-acp, with the Agent SDK's bundled CLI) and opencode (`opencode acp`),
# pinned by package-lock.json, behind one ACP client. Build context is agent-host/.
#
# The host runs as root only to start each session as its own Unix user (R3): 32 users cs0…cs31 (uids 20000–20031,
# CADENCE_SESSION_UIDS) exist for that and own nothing but their session's directory. Compose puts the container on
# an internal network whose only way out is the egress proxy (R4).
FROM node:22-slim
ARG CADENCE_VERSION=dev
LABEL org.opencontainers.image.version=$CADENCE_VERSION
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/* /usr/share/doc /usr/share/man \
 && for i in $(seq 0 31); do \
      uid=$((20000 + i)); \
      groupadd --gid "$uid" "cs$i" && useradd --uid "$uid" --gid "$uid" --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin "cs$i"; \
    done \
 && mkdir -p /var/lib/cadence-host /agent-credentials /host-credential \
 && chmod 0711 /var/lib/cadence-host && chmod 0700 /agent-credentials
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force
COPY tsconfig.json ./
COPY src ./src
COPY docker-entrypoint.sh /usr/local/bin/cadence-agent-host
ENV CADENCE_VERSION=$CADENCE_VERSION \
    CADENCE_HOST_DATA=/var/lib/cadence-host \
    CADENCE_AGENT_CREDENTIALS=/agent-credentials \
    CADENCE_SESSION_UIDS=20000:32
ENTRYPOINT ["cadence-agent-host"]
CMD ["host"]
