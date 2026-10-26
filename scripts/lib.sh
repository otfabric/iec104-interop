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

# DOCKER_ARGS: extra "docker run" arguments of start_server and run_client,
# such as a mount: DOCKER_ARGS=(-v "${dir}:/secrets:ro").
DOCKER_ARGS=()

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
        --label "iec104-interop.run=${RUN_ID}" ${DOCKER_ARGS[@]+"${DOCKER_ARGS[@]}"} \
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
        ${DOCKER_ARGS[@]+"${DOCKER_ARGS[@]}"} "$(image_of "${adapter}")" client "$@" 2>/dev/null
}

# make_identities <dir>: key material for the security flags, made with
# openssl, valid for two days. Every key is on secp256r1.
#   ca            an authority (CA:TRUE)
#   station       the controlled station, signed by ca
#   scada         a controlling station, signed by ca
#   rogue         a controlling station nobody trusts (self-signed)
#   pin-station   self-signed controlled station, trusted by pin
#   pin-scada     self-signed controlling station, trusted by pin
# Mount <dir> read-only; the files are readable by the uid of the images.
make_identities() {
    local dir="$1" n
    command -v openssl >/dev/null || { log "missing required tool: openssl"; return 1; }
    mkdir -p "${dir}" && chmod 755 "${dir}"
    openssl req -x509 -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
        -keyout "${dir}/ca.key" -out "${dir}/ca.pem" -subj "/CN=iec104-interop CA" \
        -addext basicConstraints=critical,CA:TRUE -addext keyUsage=critical,keyCertSign 2>/dev/null || return 1
    for n in station scada; do
        openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
            -keyout "${dir}/${n}.key" -out "${dir}/${n}.csr" -subj "/CN=${n}" 2>/dev/null || return 1
        openssl x509 -req -in "${dir}/${n}.csr" -CA "${dir}/ca.pem" -CAkey "${dir}/ca.key" -days 2 \
            -out "${dir}/${n}.pem" \
            -extfile <(printf 'basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyAgreement\n') \
            2>/dev/null || return 1
    done
    for n in rogue pin-station pin-scada; do
        openssl req -x509 -new -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 2 \
            -keyout "${dir}/${n}.key" -out "${dir}/${n}.pem" -subj "/CN=${n}" \
            -addext basicConstraints=critical,CA:FALSE 2>/dev/null || return 1
    done
    rm -f "${dir}"/*.csr
    chmod 644 "${dir}"/*
}

# What two adapters must agree on: everything except who answered, how long it
# took, the wall-clock time tags and the NOS of the last-section F_LS_NA_1
# (LSQ 1 or 2), which the stacks fill differently (docs/FIXTURES.md).
# shellcheck disable=SC2034
NORMALIZE='del(.adapter, .elapsedMs) | (.asdus[]?.objects[]? |= del(.time))
    | (.asdus[]? |= if .type == "F_LS_NA_1" then (.objects[]? |= if .lsq == 1 or .lsq == 2 then del(.nos) else . end) else . end)'

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
