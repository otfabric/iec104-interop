#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Turn the records of the self-tests into the organisation's interop-result-v1.

    scripts/write-results.py <directory> [<results.json>]

Reads every results-*.jsonl in the directory (scripts/results.sh writes them)
and writes results.json there. Names of implementations are those of
versions.yaml. The exit status is 1 when the run failed: a fail or infra-error,
a waiver that no longer reproduces, or no pass at all (scripts/interop_result.py).
"""

import glob
import json
import os
import re
import sys

import interop_result

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
REPOSITORY = "otfabric/iec104-interop"
OPTIONAL = ("title", "feature", "protocolVersion", "platform", "evidence")


def versions_yaml(root=ROOT):
    """name -> the scalars of its block under "implementations:" in versions.yaml."""
    out, name, inside = {}, None, False
    with open(os.path.join(root, "versions.yaml")) as f:
        for line in f:
            if re.match(r"\S", line):
                inside = line.startswith("implementations:")
                continue
            m = re.match(r"  ([A-Za-z0-9_-]+):\s*$", line)
            if inside and m:
                name = m.group(1)
                out[name] = {}
                continue
            m = re.match(r'    (upstream|version|revision|license): "([^"]*)"', line)
            if inside and name and m:
                out[name][m.group(1)] = m.group(2)
    return out


def load(directory):
    records = []
    for path in sorted(glob.glob(os.path.join(directory, "results-*.jsonl"))):
        with open(path) as f:
            records += [json.loads(line) for line in f if line.strip()]
    return records


def implementation(name, pins, image):
    pin = pins.get(name, {})
    i = {"name": name, "version": pin.get("version") or pin.get("revision", "unknown")[:12], "independent": True}
    for key in ("upstream", "revision", "license"):
        if pin.get(key):
            i[key] = pin[key]
    digest = image(name) if image else None
    if digest:
        i["image"] = digest
    return i


def convert(records, pins, image=None):
    results, waivers, runs = [], {}, []
    for r in records:
        if r["kind"] == "run":
            runs.append(r)
        elif r["kind"] == "waiver":
            w = waivers.setdefault(r["id"], {k: r[k] for k in ("id", "scope", "evidence")} | {"reproduced": False})
            w["reproduced"] = w["reproduced"] or r["reproduced"]
        else:
            roles = {k: r[k] for k in ("client", "server") if r.get(k)} or {"harness": "selftest"}
            res = {"id": r["id"], "suite": r["id"].split(".")[0], "roles": roles, "outcome": r["outcome"],
                   "durationMs": max(r["durationMs"], 0)}
            res.update({k: r[k] for k in OPTIONAL if r.get(k)})
            if r["outcome"] != "pass":
                res["reason"] = r.get("reason") or r["outcome"]
            if r.get("waiver"):
                res["waiver"] = r["waiver"]
            results.append(res)
    names = sorted({n for r in results for k, n in r["roles"].items() if k != "harness"})
    return {"schemaVersion": "1.0", "repository": REPOSITORY,
            "interopVersion": interop_result.version_field(ROOT, "interop_version"),
            "contractVersion": interop_result.version_field(ROOT, "contract_version"),
            "gitSha": interop_result.git_sha(ROOT),
            "startedAt": min((r["startedAt"] for r in runs), default=interop_result.now()),
            "finishedAt": interop_result.now(), "host": interop_result.host(),
            "implementations": [implementation(n, pins, image) for n in names] or [{"name": "none", "version": "none"}],
            "waivers": list(waivers.values()),
            "reproduce": " && ".join(dict.fromkeys(r["reproduce"] for r in runs)) or "none", "results": results}


def image_of(name):
    """The digest of the image the run used; a local build has none."""
    template = os.environ.get("RESULT_IMAGE", "ghcr.io/otfabric/" + REPOSITORY.split("/")[1] + "-{}:" + os.environ.get("VERSION", "dev"))
    return interop_result.repo_digest(template.format(name))


def main(argv):
    if not 1 <= len(argv) <= 2:
        print(__doc__, file=sys.stderr)
        return 2
    out = argv[1] if len(argv) == 2 else os.path.join(argv[0], "results.json")
    doc = convert(load(argv[0]), versions_yaml(), image_of)
    interop_result.write(out, doc)
    bad = ["schema: " + e for e in interop_result.schema_errors(doc)] + interop_result.problems(doc)
    print("%s: %s" % (out, ", ".join("%d %s" % (doc["summary"][o], o) for o in interop_result.OUTCOMES)), file=sys.stderr)
    for p in bad:
        print("FAIL " + p, file=sys.stderr)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
