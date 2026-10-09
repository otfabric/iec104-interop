#!/usr/bin/env bash
# Container-contract smoke test for one adapter image.
#
# Usage: smoke.sh <adapter>          e.g. smoke.sh lib60870
#
# Checks the parts of docs/CONTAINER_CONTRACT.md that do not need a second
# adapter: the command surface, capabilities, the baked fixture, readiness,
# the server event stream, exit codes and a clean stop on SIGTERM.
#
# Environment: REGISTRY, VERSION (image tag, default dev), VERBOSE=1,
#              REPORT_DIR (keeps the capability document when set).

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/lib.sh"

ADAPTER="${1:?usage: smoke.sh <adapter>}"
IMAGE="$(image_of "${ADAPTER}")"
require_tools
trap cleanup EXIT
network_up
log "${IMAGE}"

expect_exit() {  # expect_exit <code> <label> <docker run args...>
    local want="$1" label="$2"; shift 2
    docker run --rm --network "${NETWORK}" --label "iec104-interop.run=${RUN_ID}" "$@" >/dev/null 2>&1
    local got=$?
    if [[ "${got}" == "${want}" ]]; then ok "${label}: exit ${got}"; else fail "${label}: exit ${got}, want ${want}"; fi
}

# ---- capabilities ----
caps=$(docker run --rm "${IMAGE}" print-capabilities 2>/dev/null)
if [[ -n "${REPORT_DIR:-}" ]]; then
    mkdir -p "${REPORT_DIR}"
    printf '%s\n' "${caps}" > "${REPORT_DIR}/capabilities-${ADAPTER}.json"
fi
if jq -e --arg a "${ADAPTER}" '
        .schemaVersion == "1.0" and .adapter == $a and .protocol == "iec60870-5-104"
        and (.adapterVersion | type == "string") and (.upstream.name | type == "string")
        and (.upstream.version | type == "string") and (.upstream.license | type == "string")
        and (.upstream.independentEngine | type == "boolean")
        and (.roles.server | type == "boolean") and (.roles.client | type == "boolean")
        and (.features | type == "object") and ([.features[] | type == "boolean"] | all)
        and (.pointTypes | type == "array") and (.commandTypes | type == "array")
        and (.clientOperations | type == "array")' <<<"${caps}" >/dev/null 2>&1
then ok "print-capabilities"; else fail "print-capabilities is not a valid capability document: ${caps}"; fi
if [[ $(wc -l <<<"${caps}") -eq 1 ]]; then ok "print-capabilities is one line"; else fail "print-capabilities printed more than one line"; fi

# ---- the baked fixture is the repository's ----
if diff -q <(docker run --rm "${IMAGE}" print-fixture baseline 2>/dev/null) "${ROOT}/fixtures/baseline/fixture.json" >/dev/null
then ok "print-fixture baseline"; else fail "print-fixture baseline differs from fixtures/baseline/fixture.json"; fi
expect_exit 1 "print-fixture of an unknown name" "${IMAGE}" print-fixture no-such-fixture

# ---- usage errors never touch the network ----
# (Without a command the image runs "server": that is its default.)
expect_exit 2 "unknown command" "${IMAGE}" frobnicate
expect_exit 2 "client without --host" "${IMAGE}" client interrogate
expect_exit 2 "client with an unknown operation" "${IMAGE}" client frobnicate --host nowhere
expect_exit 2 "client with an unknown flag" "${IMAGE}" client interrogate --host nowhere --frobnicate 1
expect_exit 2 "command without --value" "${IMAGE}" client command --host nowhere --type C_SC_NA_1 --ioa 500
expect_exit 2 "command with a bad value" "${IMAGE}" client command --host nowhere --type C_SE_NB_1 --ioa 504 --value 99999
expect_exit 2 "server with an unknown flag" "${IMAGE}" server --frobnicate
expect_exit 2 "server with invalid APCI parameters" "${IMAGE}" server --k 4 --w 9

# ---- an invalid fixture stops the server before it listens ----
bad=$(mktemp -d)
trap 'rm -rf "${bad}"; cleanup' EXIT
chmod 755 "${bad}"
jq '.commands[0].target = 300' "${ROOT}/fixtures/baseline/fixture.json" > "${bad}/fixture.json"
chmod 644 "${bad}/fixture.json"
expect_exit 4 "server with an invalid fixture" -v "${bad}:/bad:ro" "${IMAGE}" server --fixture /bad/fixture.json
expect_exit 4 "server with a missing fixture" "${IMAGE}" server --fixture /no/such/fixture.json

# ---- a connection that cannot be made ----
doc=$(run_client "${ADAPTER}" interrogate --host "no-such-host-${RUN_ID}" --connect-timeout-ms 1500); CLIENT_EXIT=$?
if [[ "${CLIENT_EXIT}" == 3 ]] && jq -e '.ok == false and .error.code == "connect-failed" and .connected == false' <<<"${doc}" >/dev/null 2>&1
then ok "connect failure: exit 3 and a result document"; else fail "connect failure: exit ${CLIENT_EXIT}, ${doc}"; fi

# ---- server: readiness, events, operations, clean stop ----
name=$(start_server "${ADAPTER}")
if wait_ready "${name}"; then ok "server becomes ready"; else fail "server did not become ready"; finish; exit 1; fi
first=$(docker logs "${name}" 2>/dev/null | head -1)
if jq -e --arg a "${ADAPTER}" '.event == "ready" and .adapter == $a and .fixture == "baseline"
        and .commonAddress == 1 and (.address | type == "string")' <<<"${first}" >/dev/null 2>&1
then ok "ready event"; else fail "first stdout line is not the ready event: ${first}"; fi

doc=$(run_client "${ADAPTER}" interrogate --host "${name}"); CLIENT_EXIT=$?
if [[ "${CLIENT_EXIT}" == 0 ]] && jq -e --arg a "${ADAPTER}" '
        .schemaVersion == "1.0" and .adapter == $a and .operation == "interrogate" and .ok == true
        and .error == null and .connected and .startdtConfirmed and .terminated
        and (.confirmations == [{"cot":7,"negative":false}]) and (.elapsedMs | type == "number")
        and ([.asdus[] | (.type, .typeId, .cot, .negative, .test, .originator, .commonAddress, .sequence, .count, .objects) != null] | all)' <<<"${doc}" >/dev/null 2>&1
then ok "client result document"; else fail "interrogate: exit ${CLIENT_EXIT}, ${doc}"; fi
if [[ $(wc -l <<<"${doc}") -eq 1 ]]; then ok "client result is one line"; else fail "client printed more than one line on stdout"; fi

run_client "${ADAPTER}" command --type C_SC_NA_1 --ioa 500 --value false --host "${name}" >/dev/null
run_client "${ADAPTER}" clock-sync --time 2026-10-09T15:35:12.345Z --host "${name}" >/dev/null
sleep 0.3
events=$(docker logs "${name}" 2>/dev/null)
if grep -v '^{' <<<"${events}" | grep -q .; then fail "server stdout has lines that are not JSON"; else ok "server stdout is JSON Lines only"; fi
for want in \
    '.event == "connection-opened" and (.peer | type == "string")' \
    '.event == "data-transfer-started"' \
    '.event == "interrogation" and .qoi == 20 and .commonAddress == 1 and .accepted' \
    '.event == "command" and .type == "C_SC_NA_1" and .ioa == 500 and .value == false and .select == false and .cot == 6 and .outcome == "executed"' \
    '.event == "clock-sync" and .time == "2026-10-09T15:35:12.345Z"' \
    '.event == "connection-closed"'
do
    if jq -e -s "map(select(${want})) | length > 0" <<<"${events}" >/dev/null 2>&1; then ok "event: ${want}"; else fail "no server event with ${want}"; fi
done

docker stop -t 10 "${name}" >/dev/null
code=$(docker inspect --format '{{.State.ExitCode}}' "${name}")
last=$(docker logs "${name}" 2>/dev/null | tail -1)
if [[ "${code}" == 0 ]]; then ok "SIGTERM: exit 0"; else fail "SIGTERM: exit ${code}"; fi
if jq -e '.event == "stopped"' <<<"${last}" >/dev/null 2>&1; then ok "stopped event"; else fail "last stdout line is not the stopped event: ${last}"; fi
stop_server "${name}"

# ---- a port that is already taken ----
name=$(start_server "${ADAPTER}"); wait_ready "${name}"
docker exec "${name}" /usr/local/bin/iec104-interop server --ready-file /tmp/second >/dev/null 2>&1
code=$?
if [[ "${code}" != 0 ]]; then ok "second server on a busy port fails (exit ${code})"; else fail "second server on a busy port exited 0"; fi
stop_server "${name}"

finish
