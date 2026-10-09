#!/usr/bin/env bash
# Reference-stack self-test: every client adapter against every server adapter.
#
# Usage: interop.sh <adapter> [<adapter> ...]      e.g. interop.sh lib60870 openmuc
#
# For each case in tests/cases.tsv and each client/server pairing:
#   - the outcome must match the expectation of the case, and
#   - all pairings must return the same result document (modulo time tags).
# Then checks on values and on connection handling rather than outcomes:
#   - a general interrogation returns exactly the points of the fixture,
#   - a command changes what a later interrogation reports,
#   - k = w = 1, an idle connection and concurrent sessions work.
#
# tests/known-bugs.tsv lists bugs of specific upstream versions; a case they
# explain is counted as a known bug instead of a failure.
#
# This tests the reference adapters against each other. It says nothing about
# any library that consumes the images.
#
# Environment: REGISTRY, VERSION (image tag, default dev), VERBOSE=1,
#              REPORT_DIR (keeps every result document when set).

set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/lib.sh"

[[ $# -ge 1 ]] || { echo "usage: interop.sh <adapter> [<adapter> ...]" >&2; exit 2; }
ADAPTERS=("$@")
FIXTURE="${ROOT}/fixtures/baseline/fixture.json"
CASES="${ROOT}/tests/cases.tsv"
KNOWN_BUGS="${ROOT}/tests/known-bugs.tsv"
require_tools
WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"; cleanup' EXIT
network_up

# ---- known upstream bugs that apply to the versions under test ----
# ${WORK}/active: "<adapter>\t<operation regex>\t<description>" per line.
: > "${WORK}/active"
: > "${WORK}/reproduced"
for adapter in "${ADAPTERS[@]}"; do
    docker run --rm "$(image_of "${adapter}")" print-capabilities > "${WORK}/caps-${adapter}.json" 2>/dev/null
    version=$(jq -r '.upstream.version' "${WORK}/caps-${adapter}.json")
    log "${adapter}: upstream ${version}"
    while IFS=$'\t' read -r bug_adapter bug_version bug_regex bug_text; do
        [[ -z "${bug_adapter}" || "${bug_adapter}" == \#* ]] && continue
        if [[ "${bug_adapter}" == "${adapter}" && "${bug_version}" == "${version}" ]]; then
            printf '%s\t%s\t%s\n' "${bug_adapter}" "${bug_regex}" "${bug_text}" >> "${WORK}/active"
            log "known bug in ${adapter} ${version}: ${bug_text}"
        fi
    done < "${KNOWN_BUGS}"
done

# has <adapter> <feature>: the adapter declares the feature.
has() { jq -e --arg f "$2" '.features[$f] == true' "${WORK}/caps-$1.json" >/dev/null 2>&1; }

# has_role <adapter> <server|client>
has_role() { jq -e --arg r "$2" '.roles[$r] == true' "${WORK}/caps-$1.json" >/dev/null 2>&1; }

# supported <client> <server> <needs>: the pairing declares every feature in
# the comma-separated list of "server:<feature>" and "client:<feature>".
supported() {
    local need side feature
    has_role "$1" client && has_role "$2" server || return 1
    [[ "$3" == "-" ]] && return 0
    for need in ${3//,/ }; do
        side="${need%%:*}"; feature="${need#*:}"
        if [[ "${side}" == "server" ]]; then has "$2" "${feature}" || return 1
        else has "$1" "${feature}" || return 1; fi
    done
    return 0
}

SKIPPED=0
skip() { SKIPPED=$((SKIPPED + 1)); [[ -n "${VERBOSE:-}" ]] && log "skip: $*"; return 0; }

# known_bug <client> <server> <operation>: prints the description of a known
# bug that may affect this pairing and operation, if there is one.
known_bug() {
    local bug_adapter bug_regex bug_text
    while IFS=$'\t' read -r bug_adapter bug_regex bug_text; do
        if [[ "${bug_adapter}" == "$1" || "${bug_adapter}" == "$2" ]] && [[ "$3" =~ ${bug_regex} ]]; then
            echo "${bug_text}"
            return 0
        fi
    done < "${WORK}/active"
    return 1
}

# hit_known_bug <pair> <operation> <description>
hit_known_bug() {
    KNOWN=$((KNOWN + 1))
    echo "$3" >> "${WORK}/reproduced"
    [[ -n "${VERBOSE:-}" ]] && log "known bug: $1: $2"
    return 0
}

# One server per adapter is shared by the read-only cases; cases that change
# the station get a server of their own.
shared_server() { SERVER_TAG=shared server_name "$1"; }
for adapter in "${ADAPTERS[@]}"; do
    name=$(SERVER_TAG=shared start_server "${adapter}")
    wait_ready "${name}" || exit 1
done

keep() {  # keep <label> <document>
    [[ -n "${REPORT_DIR:-}" ]] || return 0
    mkdir -p "${REPORT_DIR}"
    printf '%s\n' "$2" > "${REPORT_DIR}/$(echo "$1" | tr -c 'A-Za-z0-9._\n-' '_').json"
}

log "adapters: ${ADAPTERS[*]} (${VERSION})"
n=0
while IFS=$'\t' read -r state want_exit want_summary needs operation; do
    [[ -z "${state}" || "${state}" == \#* ]] && continue
    n=$((n + 1))
    reference="" reference_pair=""
    for server in "${ADAPTERS[@]}"; do
        for client in "${ADAPTERS[@]}"; do
            pair="${client}->${server}"
            if ! supported "${client}" "${server}" "${needs}"; then
                skip "${pair}: ${operation} (needs ${needs})"
                continue
            fi
            if [[ "${state}" == "rw" ]]; then
                name=$(start_server "${server}"); wait_ready "${name}" || exit 1
            else
                name=$(shared_server "${server}")
            fi
            # shellcheck disable=SC2086
            doc=$(run_client "${client}" ${operation} --host "${name}"); CLIENT_EXIT=$?
            [[ "${state}" == "rw" ]] && stop_server "${name}"
            keep "case${n}-${pair}" "${doc}"

            bug=$(known_bug "${client}" "${server}" "${operation}")
            summary=$(jq -r "${SUMMARY}" <<<"${doc}" 2>/dev/null)
            if [[ "${summary}" != "${want_summary}" || "${CLIENT_EXIT}" != "${want_exit}" ]]; then
                if [[ -n "${bug}" ]]; then
                    hit_known_bug "${pair}" "${operation}" "${bug}"
                    continue
                fi
                fail "${pair}: ${operation}"
                log "    want exit=${want_exit} ${want_summary}"
                log "    got  exit=${CLIENT_EXIT} ${summary:-<no result document>}"
                continue
            fi
            ok "${pair}: ${operation}"
            # A pairing a known bug may affect is not held to agreement.
            [[ -n "${bug}" ]] && continue

            normalized=$(jq -cS "${NORMALIZE}" <<<"${doc}")
            if [[ -z "${reference}" ]]; then
                reference="${normalized}"; reference_pair="${pair}"
            elif [[ "${normalized}" != "${reference}" ]]; then
                fail "${pair} and ${reference_pair} disagree on: ${operation}"
                diff <(jq -S . <<<"${reference}") <(jq -S . <<<"${normalized}") | head -20 >&2
            else
                ok "${pair} agrees with ${reference_pair}: ${operation}"
            fi
        done
    done
done < "${CASES}"

# ---- values: an interrogation returns the fixture ----
want_points=$(jq -cS '[.points[] | select(.type != "M_IT_NA_1")
    | {ioa, type, value, quality: (.quality // [])}] | sort_by(.ioa)' "${FIXTURE}")
want_counters=$(jq -cS '[.points[] | select(.type == "M_IT_NA_1")
    | {ioa, type, value, quality: (.quality // [])}] | sort_by(.ioa)' "${FIXTURE}")
got_points='[.asdus[] | select(.cot == $cot) | .type as $t | .objects[] | {ioa, type: $t, value, quality}] | sort_by(.ioa)'
for server in "${ADAPTERS[@]}"; do
    name=$(shared_server "${server}")
    for client in "${ADAPTERS[@]}"; do
        pair="${client}->${server}"
        got=$(run_client "${client}" interrogate --host "${name}" | jq -cS --argjson cot 20 "${got_points}")
        if [[ "${got}" == "${want_points}" ]]; then ok "${pair}: interrogation matches the fixture"
        else fail "${pair}: interrogation does not match the fixture"; log "    want ${want_points}"; log "    got  ${got}"; fi
        got=$(run_client "${client}" counter-interrogate --host "${name}" | jq -cS --argjson cot 37 "${got_points}")
        if [[ "${got}" == "${want_counters}" ]]; then ok "${pair}: counter interrogation matches the fixture"
        else fail "${pair}: counter interrogation does not match the fixture"; log "    want ${want_counters}"; log "    got  ${got}"; fi
    done
done

# ---- values: a command changes what the station reports ----
value_of='[.asdus[] | .objects[] | select(.ioa == $ioa) | .value] | first'
for server in "${ADAPTERS[@]}"; do
    for client in "${ADAPTERS[@]}"; do
        pair="${client}->${server}"
        name=$(start_server "${server}"); wait_ready "${name}" || exit 1
        report=$(run_client "${client}" command --type C_SE_NC_1 --ioa 502 --value 49.5 --host "${name}" \
            | jq -c '[.asdus[] | select(.type == "M_ME_TF_1") | {cot, ioa: .objects[0].ioa, value: .objects[0].value, timed: (.objects[0].time != null)}]')
        if [[ "${report}" == '[{"cot":11,"ioa":202,"value":49.5,"timed":true}]' ]]; then ok "${pair}: set point is reported"
        else fail "${pair}: set point report is ${report}"; fi
        after=$(run_client "${client}" read --ioa 202 --host "${name}" | jq -c --argjson ioa 202 "${value_of}")
        if [[ "${after}" == "49.5" ]]; then ok "${pair}: set point is visible to a later read"
        elif bug=$(known_bug "${client}" "${server}" "read --ioa 202"); then hit_known_bug "${pair}" "read after set point" "${bug}"
        else fail "${pair}: read after set point returns ${after}"; fi
        run_client "${client}" command --type C_SC_NA_1 --ioa 500 --value false --host "${name}" >/dev/null
        after=$(run_client "${client}" interrogate --host "${name}" | jq -c --argjson ioa 100 "${value_of}")
        if [[ "${after}" == "false" ]]; then ok "${pair}: single command is visible to a later interrogation"
        else fail "${pair}: interrogation after single command returns ${after}"; fi
        stop_server "${name}"
    done
done

# ---- sessions: flow control windows, idle test, concurrent connections ----
for server in "${ADAPTERS[@]}"; do
    # k = w = 1 on both sides: one I frame outstanding at a time, each one
    # acknowledged before the next. The timers are short so that the idle test
    # below runs several times.
    name=$(SERVER_TAG=tight start_server "${server}" --k 1 --w 1 --t1 2 --t2 1 --t3 2)
    wait_ready "${name}" || exit 1
    for client in "${ADAPTERS[@]}"; do
        pair="${client}->${server}"
        got=$(run_client "${client}" interrogate --k 1 --w 1 --host "${name}" | jq -cS --argjson cot 20 "${got_points}")
        if [[ "${got}" == "${want_points}" ]]; then ok "${pair}: interrogation with k=1 w=1"
        else fail "${pair}: interrogation with k=1 w=1 returned ${got}"; fi

        # Idle for longer than t3 + t1: only confirmed test frames keep it up.
        doc=$(run_client "${client}" connect --hold-ms 6000 --t1 2 --t2 1 --t3 2 --host "${name}")
        if jq -e '.ok and .stopdtConfirmed' <<<"${doc}" >/dev/null 2>&1; then ok "${pair}: idle connection survives t3"
        else fail "${pair}: idle connection: $(jq -r "${SUMMARY}" <<<"${doc}" 2>/dev/null)"; fi
    done
    stop_server "${name}"

    name=$(shared_server "${server}")
    for client in "${ADAPTERS[@]}"; do
        pair="${client}->${server}"
        tmp=$(mktemp -d)
        for i in 1 2 3; do
            run_client "${client}" interrogate --collect-ms 500 --host "${name}" > "${tmp}/${i}.json" &
        done
        wait
        good=0
        for i in 1 2 3; do
            got=$(jq -cS --argjson cot 20 "${got_points}" "${tmp}/${i}.json" 2>/dev/null)
            [[ "${got}" == "${want_points}" ]] && good=$((good + 1))
        done
        rm -rf "${tmp}"
        if [[ "${good}" == 3 ]]; then ok "${pair}: three concurrent sessions"
        else fail "${pair}: only ${good} of 3 concurrent sessions returned the fixture"; fi
    done
done

# ---- values: a downloaded file is the fixture's ----
while IFS=$'\t' read -r file_ioa file_name file_size; do
    want_hash=$(python3 -c 'import hashlib,sys; ioa,size=int(sys.argv[1]),int(sys.argv[2]); print(hashlib.sha256(bytes((i+ioa)%251 for i in range(size))).hexdigest())' "${file_ioa}" "${file_size}")
    for server in "${ADAPTERS[@]}"; do
        for client in "${ADAPTERS[@]}"; do
            pair="${client}->${server}"
            supported "${client}" "${server}" "server:fileServer,client:fileClient" || { skip "${pair}: file ${file_ioa}"; continue; }
            got=$(run_client "${client}" file-get --ioa "${file_ioa}" --name "${file_name}" --host "$(shared_server "${server}")" \
                | jq -c '[.file.length, .file.received, .file.sha256]')
            if [[ "${got}" == "[${file_size},${file_size},\"${want_hash}\"]" ]]; then ok "${pair}: file ${file_ioa} has the fixture's content"
            else fail "${pair}: file ${file_ioa}: got ${got}, want ${file_size} octets with sha256 ${want_hash}"; fi
        done
    done
done < <(jq -r '.files[]? | [.ioa, .name, .size] | @tsv' "${FIXTURE}")

# ---- a known bug that no longer shows is a stale entry ----
while IFS=$'\t' read -r bug_adapter bug_regex bug_text; do
    if ! grep -qxF "${bug_text}" "${WORK}/reproduced"; then
        fail "known bug of ${bug_adapter} did not reproduce: ${bug_text}"
        log "    remove it from tests/known-bugs.tsv; the pin can move to this version"
    fi
done < "${WORK}/active"

finish
