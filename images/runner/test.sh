#!/usr/bin/env bash
set -euo pipefail
image=${1:?runner image is required}
python3 "$(dirname "$0")/test_snapshot.py" "$(dirname "$0")/flatrun-snapshot"
volume="flatrun-runner-smoke-$(date +%s)-$$"
cleanup() { docker volume rm "$volume" > /dev/null; }
trap cleanup EXIT
docker volume create "$volume" > /dev/null
docker run --rm --cpus 2 --memory 4g "$image" bash -euc '
    gcc --version; make --version; google-chrome --version
    flatrun-snapshot --help > /dev/null
    psql --version; pdftotext -v; pdfinfo -v; jq --version; xz --version; zip -v
    docker compose version; docker buildx version; hadolint --version
    sudo -n true
    pgdata=$(mktemp -d)/postgres
    initdb -D "$pgdata" -U runner --auth=trust > /dev/null
    trap '\''pg_ctl -D "$pgdata" -m immediate --wait stop > /dev/null'\'' EXIT
    pg_ctl -D "$pgdata" -l /tmp/postgres.log -o "-c listen_addresses=127.0.0.1 -k /tmp" --wait start
    test "$(psql -h 127.0.0.1 -U runner -d postgres -Atc "SELECT 42")" = 42
'
docker run --rm --security-opt no-new-privileges "$image" bash -euc '
    if sudo -n true; then echo "sudo unexpectedly succeeded" >&2; exit 1; fi
'
for pass in populate offline; do
    docker run --rm --cpus 2 --memory 4g --security-opt no-new-privileges \
        --mount "type=volume,source=$volume,target=/flatrun-cache" \
        -e RUNNER_TOOL_CACHE=/flatrun-cache/tools -e GOMODCACHE=/flatrun-cache/go-mod \
        -e GOCACHE=/flatrun-cache/go-build -e npm_config_cache=/flatrun-cache/npm \
        -e SMOKE_PASS="$pass" "$image" bash -euc '
            go_path=$(find "$RUNNER_TOOL_CACHE/go" -mindepth 1 -maxdepth 1 -type d | sort -V | tail -1)/x64/bin
            node_path=$(find "$RUNNER_TOOL_CACHE/node" -mindepth 1 -maxdepth 1 -type d | sort -V | tail -1)/x64/bin
            export PATH="$go_path:$node_path:$PATH"
            go version; node --version; npm --version
            if [[ "$SMOKE_PASS" == populate ]]; then
                go mod download github.com/google/uuid@v1.6.0
                npm cache add is-number@7.0.0
                printf ready > /flatrun-cache/marker
            else
                test "$(cat /flatrun-cache/marker)" = ready
                GOPROXY=off GOSUMDB=off go mod download github.com/google/uuid@v1.6.0
                npm cache add --offline is-number@7.0.0
            fi
        '
done
