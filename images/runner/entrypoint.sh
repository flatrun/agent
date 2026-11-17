#!/usr/bin/env bash
set -euo pipefail
if [[ "${RUNNER_TOOL_CACHE:-}" == /flatrun-cache/tools ]]; then
    date +%s > /flatrun-cache/.flatrun-last-used
    if [[ "${GOCACHE:-}" == /flatrun-cache/shared/go-build ]]; then
        date +%s > /flatrun-cache/shared/.flatrun-last-used
    fi
    mkdir -p "$RUNNER_TOOL_CACHE"
    cp -a --update=none /opt/hostedtoolcache/. "$RUNNER_TOOL_CACHE/"
fi
if [[ "${FLATRUN_START_DOCKER:-0}" == 1 ]]; then
    dockerd --host=unix:///var/run/docker.sock > /tmp/dockerd.log 2>&1 &
    for attempt in {1..120}; do
        if docker info > /dev/null 2>&1; then break; fi
        sleep 0.25
    done
    if ! docker info > /dev/null 2>&1; then cat /tmp/dockerd.log >&2; exit 1; fi
fi
exec "$@"
