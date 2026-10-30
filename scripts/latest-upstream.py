#!/usr/bin/env python3
"""Compares the upstream pin of one stack with the latest upstream.

    scripts/latest-upstream.py lib60870|lib60870-head|j60870|wendy512|cbsigma

Prints key=value lines (the format of $GITHUB_OUTPUT), for the Upstream
workflow:

    pin=<the release pin>
    latest=<the latest stable upstream release>
    update=yes|no|info
    source=<where latest was looked up>
    args=<make variables of the candidate: make candidate-<adapter> <args>>

lib60870-head is the tip of the default branch of lib60870, compared with the
pinned commit (commits are shown with 12 digits). A newer tip is built and
self-tested, but it is update=info: the pin follows releases, so a new
commit alone does not open the issue. The others follow stable
X.Y.Z releases. A newer tag on the same commit as the pin is the same code:
not an update. wendy512 is two modules, iec104 and its engine go-iecp5; it is
behind when either is. Nothing is modified.
"""

import re
import subprocess
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
AGENT = "iec104-interop latest-upstream (https://github.com/otfabric/iec104-interop)"
STABLE = re.compile(r"v?(\d+)\.(\d+)\.(\d+)")


def fetch(url):
    request = urllib.request.Request(url, headers={"User-Agent": AGENT})
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read().decode()


def key(version):
    """Sort key of a stable version; None for anything else."""
    match = STABLE.fullmatch(version)
    return tuple(int(part) for part in match.groups()) if match else None


def newest(versions):
    stable = [v for v in versions if key(v) is not None]
    if not stable:
        raise SystemExit("no stable release found upstream")
    return max(stable, key=key)


def arg(adapter, name):
    """ARG <name>=<value> of the adapter Dockerfile."""
    match = re.search(rf"^ARG {name}=(.+)$", (ROOT / "adapters" / adapter / "Dockerfile").read_text(), re.M)
    if not match:
        raise SystemExit(f"adapters/{adapter}/Dockerfile has no ARG {name}=<pin>")
    return match.group(1).strip()


def git_tags(repository):
    """Tags of repository as {tag: commit}; an annotated tag gives its commit."""
    out = subprocess.check_output(["git", "ls-remote", "--tags", repository], text=True)
    commits = {}
    for line in out.splitlines():
        sha, ref = line.split()
        tag = ref.removeprefix("refs/tags/")
        if tag.endswith("^{}"):
            commits[tag[:-3]] = sha
        else:
            commits.setdefault(tag, sha)
    return commits


LIB60870 = "https://github.com/mz-automation/lib60870.git"


def lib60870():
    pin, pin_sha = arg("lib60870", "LIB60870_VERSION"), arg("lib60870", "LIB60870_SHA")
    tags = git_tags(LIB60870)
    latest = newest(tags)
    args = f"LIB60870_CANDIDATE_SHA={tags[latest]} LIB60870_CANDIDATE_NAME={latest}"
    return pin, latest, tags[latest] != pin_sha, "git tags of mz-automation/lib60870", args


def lib60870_head():
    pin_sha = arg("lib60870", "LIB60870_SHA")
    tip = subprocess.check_output(["git", "ls-remote", LIB60870, "HEAD"], text=True).split()[0]
    args = f"LIB60870_CANDIDATE_SHA={tip} LIB60870_CANDIDATE_NAME=HEAD"
    return pin_sha[:12], tip[:12], tip != pin_sha, "HEAD of mz-automation/lib60870", args


def j60870():
    pin = arg("openmuc", "J60870_VERSION")
    meta = fetch("https://repo1.maven.org/maven2/org/openmuc/j60870/maven-metadata.xml")
    latest = newest(re.findall(r"<version>([^<]+)</version>", meta))
    return pin, latest, latest != pin, "Maven Central (org.openmuc:j60870)", f"J60870_CANDIDATE={latest}"


def moved(tags, pin, latest):
    """True when latest is other code than pin."""
    return tags.get(pin) != tags.get(latest)


def wendy512():
    pin, engine_pin = arg("wendy512", "IEC104_VERSION"), arg("wendy512", "GOIECP5_VERSION")
    lib = git_tags("https://github.com/wendy512/iec104.git")
    engine = git_tags("https://github.com/wendy512/go-iecp5.git")
    latest, engine_latest = newest(lib), newest(engine)
    behind = moved(lib, pin, latest) or moved(engine, engine_pin, engine_latest)
    return (f"{pin} (go-iecp5 {engine_pin})", f"{latest} (go-iecp5 {engine_latest})", behind,
            "git tags of wendy512/iec104 and wendy512/go-iecp5",
            f"IEC104_CANDIDATE={latest} GOIECP5_CANDIDATE={engine_latest}")


def cbsigma():
    pin = arg("cbsigma", "IEC60870_VERSION")
    tags = git_tags("https://github.com/cB-Sigma/iec60870.git")
    latest = newest(tags)
    return pin, latest, moved(tags, pin, latest), "git tags of cB-Sigma/iec60870", f"IEC60870_CANDIDATE={latest}"


RESOLVERS = {
    "lib60870": lib60870,
    "lib60870-head": lib60870_head,
    "j60870": j60870,
    "wendy512": wendy512,
    "cbsigma": cbsigma,
}


def main():
    if len(sys.argv) != 2 or sys.argv[1] not in RESOLVERS:
        raise SystemExit(f"usage: latest-upstream.py {'|'.join(RESOLVERS)}")
    pin, latest, behind, source, args = RESOLVERS[sys.argv[1]]()
    print(f"pin={pin}")
    print(f"latest={latest}")
    update = "no" if not behind else "info" if sys.argv[1] == "lib60870-head" else "yes"
    print(f"update={update}")
    print(f"source={source}")
    print(f"args={args}")


if __name__ == "__main__":
    main()
