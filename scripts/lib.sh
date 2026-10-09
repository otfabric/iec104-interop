#!/usr/bin/env bash
# Shared helpers for the self-test scripts. Source, do not execute.
#
# Servers and clients run as containers on a private Docker network and reach
# each other by container name, so no host port is published and parallel runs
# do not collide.

REGISTRY="${REGISTRY:-ghcr.io/otfabric}"
VERSION="${VERSION:-dev}"
RUN_ID="${RUN_ID:-$$}"
NETWORK="iec104-interop-${RUN_ID}"
READY_FILE="/run/iec104-interop/ready"
WAIT_TIMEOUT="${WAIT_TIMEOUT:-60}"

PASS=0
FAIL=0

log()  { echo "[$(basename "$0" .sh)] $*" >&2; }
ok()   { PASS=$((PASS + 1)); [[ -n "${VERBOSE:-}" ]] && log "ok: $*"; return 0; }
fail() { FAIL=$((FAIL + 1)); log "FAIL: $*"; return 0; }

image_of() { echo "${REGISTRY}/iec104-interop-$1:${VERSION}"; }

require_tools() {
    local t
    for t in docker jq; do
        command -v "$t" >/dev/null || { log "missing required tool: $t"; exit 2; }
    done
}

network_up() { docker network create "${NETWORK}" >/dev/null; }

cleanup() {
    docker ps -aq --filter "label=iec104-interop.run=${RUN_ID}" | xargs -r docker rm -f >/dev/null 2>&1
    docker network rm "${NETWORK}" >/dev/null 2>&1
    return 0
}

# server_name <adapter>: the container name start_server uses. SERVER_TAG
# distinguishes servers of one adapter that run at the same time.
server_name() { echo "srv-$1-${SERVER_TAG:-one}-${RUN_ID}"; }

# start_server <adapter> [server args...]  -> prints the container name
start_server() {
    local adapter="$1"; shift
    local name
    name=$(server_name "${adapter}")
    docker rm -f "${name}" >/dev/null 2>&1
    docker run -d --name "${name}" --network "${NETWORK}" \
        --label "iec104-interop.run=${RUN_ID}" \
        "$(image_of "${adapter}")" server "$@" >/dev/null
    echo "${name}"
}

# wait_ready <container>: the ready file exists only once the fixture is loaded
# and the listener is up. No fixed delays.
wait_ready() {
    local name="$1" waited=0 state
    while true; do
        state=$(docker inspect --format '{{.State.Status}}' "${name}" 2>/dev/null || echo absent)
        if [[ "${state}" != "running" ]]; then
            log "server ${name} is ${state}"; docker logs --tail 20 "${name}" >&2 2>/dev/null; return 1
        fi
        docker exec "${name}" test -f "${READY_FILE}" 2>/dev/null && return 0
        if (( waited >= WAIT_TIMEOUT * 5 )); then
            log "server ${name} not ready after ${WAIT_TIMEOUT}s"; docker logs --tail 20 "${name}" >&2; return 1
        fi
        sleep 0.2; waited=$((waited + 1))
    done
}

stop_server() { docker rm -f "$1" >/dev/null 2>&1; return 0; }

# run_client <adapter> <args...>: prints the result document and returns the
# exit code of the adapter. Capture both with
#     doc=$(run_client ...); CLIENT_EXIT=$?
run_client() {
    local adapter="$1"; shift
    docker run --rm --network "${NETWORK}" --label "iec104-interop.run=${RUN_ID}" \
        "$(image_of "${adapter}")" client "$@" 2>/dev/null
}

# What two adapters must agree on: everything except who answered, how long it
# took and the wall-clock time tags.
# shellcheck disable=SC2034
NORMALIZE='del(.adapter, .elapsedMs) | (.asdus[]?.objects[]? |= del(.time))'

# One line that states the outcome of an operation; the expectation format of
# tests/cases.tsv.
# shellcheck disable=SC2034
SUMMARY='"ok=\(.ok) error=\(.error.code // "-") conf=\([.confirmations[] | "\(.cot)\(if .negative then "n" else "" end)"] | join(",")) term=\(.terminated) asdus=\([.asdus[] | "\(.type // .typeId)/\(.cot)"] | join(","))"'

KNOWN=0

finish() {
    echo "" >&2
    local extra=""
    [[ "${KNOWN}" -gt 0 ]] && extra="${extra}, ${KNOWN} known upstream bugs"
    [[ "${SKIPPED:-0}" -gt 0 ]] && extra="${extra}, ${SKIPPED} skipped (capability not declared)"
    log "${PASS} passed, ${FAIL} failed${extra}"
    [[ "${FAIL}" -eq 0 ]]
}
