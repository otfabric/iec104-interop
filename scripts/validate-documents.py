#!/usr/bin/env python3
"""Validate the documents the self-tests kept against the contract schemas.

Usage: validate-documents.py <directory>

Run the self-tests with REPORT_DIR set to keep every document:

    REPORT_DIR=reports make smoke interop
    python3 scripts/validate-documents.py reports

A file with "roles" is a capability document, one with "operation" a client
result. Needs the jsonschema package.
"""

import json
import sys
from pathlib import Path

import jsonschema

ROOT = Path(__file__).resolve().parent.parent


def validator(name):
    schema = json.loads((ROOT / "schemas" / name).read_text())
    jsonschema.Draft202012Validator.check_schema(schema)
    return jsonschema.Draft202012Validator(schema)


def main(argv):
    if len(argv) != 1:
        print(__doc__, file=sys.stderr)
        return 2
    capabilities = validator("capabilities.schema.json")
    result = validator("client-result.schema.json")
    counts = {"capabilities": 0, "client results": 0}
    failed = 0
    for path in sorted(Path(argv[0]).glob("*.json")):
        try:
            doc = json.loads(path.read_text())
        except ValueError as e:
            print(f"FAIL {path.name}: not JSON: {e}")
            failed += 1
            continue
        if "roles" in doc:
            kind, v = "capabilities", capabilities
        elif "operation" in doc:
            kind, v = "client results", result
        else:
            print(f"FAIL {path.name}: neither a capability document nor a client result")
            failed += 1
            continue
        counts[kind] += 1
        for e in v.iter_errors(doc):
            where = "/".join(str(x) for x in e.absolute_path) or "(root)"
            print(f"FAIL {path.name}: {where}: {e.message}")
            failed += 1
    print(f"{counts['capabilities']} capability documents, {counts['client results']} client results, {failed} errors")
    if sum(counts.values()) == 0:
        print("no documents found", file=sys.stderr)
        return 1
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
