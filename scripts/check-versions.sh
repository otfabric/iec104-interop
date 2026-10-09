#!/usr/bin/env bash
# Fails when versions.yaml disagrees with what the builds actually pin.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"
status=0

need() {  # need <description> <value that must appear in versions.yaml>
    if grep -qF -- "$2" versions.yaml; then
        echo "ok   $1: $2"
    else
        echo "FAIL $1: $2 is not in versions.yaml"
        status=1
    fi
}

arg() { sed -nE "s/^ARG $2=(.*)$/\1/p" "$1" | head -1; }

need "lib60870 version"  "$(arg adapters/lib60870/Dockerfile LIB60870_VERSION)"
need "lib60870 revision" "$(arg adapters/lib60870/Dockerfile LIB60870_SHA)"
need "debian digest"     "$(arg adapters/lib60870/Dockerfile DEBIAN_DIGEST)"
need "maven digest"      "$(arg adapters/openmuc/Dockerfile MAVEN_DIGEST)"
need "jre digest"        "$(arg adapters/openmuc/Dockerfile JRE_DIGEST)"
need "j60870 version"    "version: \"$(arg adapters/openmuc/Dockerfile J60870_VERSION)\""
if [[ "$(arg adapters/openmuc/Dockerfile J60870_VERSION)" != "$(sed -nE 's|.*<j60870.version>(.*)</j60870.version>.*|\1|p' adapters/openmuc/pom.xml)" ]]; then
    echo "FAIL j60870 version: Dockerfile and pom.xml disagree"
    status=1
fi
need "gson version"      "gson: \"$(sed -nE 's|.*<gson.version>(.*)</gson.version>.*|\1|p' adapters/openmuc/pom.xml)\""

gomod() { sed -nE "s|^[[:space:]]*github.com/wendy512/$1 (v[^ ]+).*$|\\1|p" adapters/wendy512/go.mod | head -1; }
need "golang digest"     "$(arg adapters/wendy512/Dockerfile GOLANG_DIGEST)"
need "wendy512 debian"   "$(arg adapters/wendy512/Dockerfile DEBIAN_DIGEST)"
need "iec104 version"    "version: \"$(arg adapters/wendy512/Dockerfile IEC104_VERSION)\""
need "go-iecp5 version"  "version: \"$(arg adapters/wendy512/Dockerfile GOIECP5_VERSION)\""
if [[ "$(arg adapters/wendy512/Dockerfile IEC104_VERSION)" != "$(gomod iec104)" ]]; then
    echo "FAIL iec104 version: Dockerfile and go.mod disagree"
    status=1
fi
if [[ "$(arg adapters/wendy512/Dockerfile GOIECP5_VERSION)" != "$(gomod go-iecp5)" ]]; then
    echo "FAIL go-iecp5 version: Dockerfile and go.mod disagree"
    status=1
fi
exit "${status}"
