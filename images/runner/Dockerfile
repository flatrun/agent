FROM golang:1.24 AS go124
RUN version="$(go env GOVERSION)" \
    && mkdir -p "/tools/go/${version#go}/x64" \
    && cp -a /usr/local/go/. "/tools/go/${version#go}/x64/" \
    && touch "/tools/go/${version#go}/x64.complete"

FROM golang:1.25 AS go125
RUN version="$(go env GOVERSION)" \
    && mkdir -p "/tools/go/${version#go}/x64" \
    && cp -a /usr/local/go/. "/tools/go/${version#go}/x64/" \
    && touch "/tools/go/${version#go}/x64.complete"

FROM node:22-bookworm-slim AS node22
RUN version="$(node -p process.versions.node)" \
    && mkdir -p "/tools/node/$version/x64/bin" "/tools/node/$version/x64/lib" \
    && cp /usr/local/bin/node "/tools/node/$version/x64/bin/" \
    && cp -a /usr/local/lib/node_modules "/tools/node/$version/x64/lib/" \
    && ln -s ../lib/node_modules/npm/bin/npm-cli.js "/tools/node/$version/x64/bin/npm" \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js "/tools/node/$version/x64/bin/npx" \
    && touch "/tools/node/$version/x64.complete"

FROM docker:29.10.0-cli AS dockercli
FROM hadolint/hadolint:v2.12.0 AS hadolint
FROM ghcr.io/actions/actions-runner:2.338.2
LABEL dev.flatrun.retain=true
USER root
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
RUN test "$(dpkg --print-architecture)" = amd64 \
    && apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
        build-essential fonts-liberation iptables jq poppler-utils postgresql-16 python3 sudo xz-utils zip \
    && curl --fail --silent --show-error --location --max-time 180 \
        -o /tmp/chrome.deb https://dl.google.com/linux/direct/google-chrome-stable_current_amd64.deb \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends /tmp/chrome.deb \
    && rm -rf /tmp/chrome.deb /var/lib/apt/lists/*
COPY --chown=runner:docker --from=go124 /tools /opt/hostedtoolcache
COPY --chown=runner:docker --from=go125 /tools /opt/hostedtoolcache
COPY --chown=runner:docker --from=node22 /tools /opt/hostedtoolcache
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=dockercli /usr/local/libexec/docker/cli-plugins/ /usr/local/lib/docker/cli-plugins/
COPY --from=hadolint /bin/hadolint /usr/local/bin/hadolint
COPY entrypoint.sh /usr/local/bin/flatrun-runner
COPY flatrun-snapshot /usr/local/bin/flatrun-snapshot
RUN chmod 0755 /usr/local/bin/flatrun-runner /usr/local/bin/flatrun-snapshot \
    && mkdir -p /flatrun-cache/tools /flatrun-cache/shared /home/runner/_work \
    && chown runner:docker /flatrun-cache /flatrun-cache/tools /flatrun-cache/shared /home/runner/_work
ENV PATH="/usr/lib/postgresql/16/bin:${PATH}"
USER runner
ENTRYPOINT ["/usr/local/bin/flatrun-runner"]
