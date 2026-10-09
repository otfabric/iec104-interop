#!/usr/bin/env python3
"""Validate every fixtures/*/fixture.json.

Two layers, both required:

  1. The structural rules of fixtures/schema/iec104-fixture.schema.json. They
     are checked with the `jsonschema` package when it is installed; CI installs
     it, so a missing package is an error there (--require-schema).
  2. The cross-field rules a JSON Schema cannot express: unique addresses, and
     commands whose target exists and has the matching type.

The adapters apply the same rules when they load a fixture; this script makes
a broken fixture fail before any image is built.

Usage: validate-fixtures.py [--require-schema] [fixture.json ...]
"""

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
SCHEMA = ROOT / "fixtures" / "schema" / "iec104-fixture.schema.json"

# Command type -> the point type it acts on.
COMMAND_TARGET = {
    "C_SC_NA_1": "M_SP_NA_1",
    "C_DC_NA_1": "M_DP_NA_1",
    "C_RC_NA_1": "M_ST_NA_1",
    "C_SE_NA_1": "M_ME_NA_1",
    "C_SE_NB_1": "M_ME_NB_1",
    "C_SE_NC_1": "M_ME_NC_1",
}


def semantic_errors(fx):
    errors = []
    points = {}
    for p in fx.get("points", []):
        ioa = p.get("ioa")
        if ioa in points:
            errors.append(f"point {ioa}: duplicate information object address")
        points[ioa] = p
    seen = set()
    for c in fx.get("commands", []):
        ioa = c.get("ioa")
        if ioa in seen:
            errors.append(f"command {ioa}: duplicate information object address")
        seen.add(ioa)
        if ioa in points:
            errors.append(f"command {ioa}: address is also used by a point")
        target = points.get(c.get("target"))
        want = COMMAND_TARGET.get(c.get("type"))
        if target is None:
            errors.append(f"command {ioa}: target {c.get('target')} is not a point")
        elif target["type"] != want:
            errors.append(f"command {ioa}: {c['type']} needs a {want} target, {c['target']} is {target['type']}")
    return errors


def main(argv):
    require_schema = "--require-schema" in argv
    paths = [Path(a) for a in argv if not a.startswith("--")]
    if not paths:
        paths = sorted((ROOT / "fixtures").glob("*/fixture.json"))
    if not paths:
        print("no fixtures found", file=sys.stderr)
        return 1

    validator = None
    try:
        import jsonschema

        schema = json.loads(SCHEMA.read_text())
        jsonschema.Draft202012Validator.check_schema(schema)
        validator = jsonschema.Draft202012Validator(schema)
    except ImportError:
        if require_schema:
            print("the jsonschema package is required (pip install jsonschema)", file=sys.stderr)
            return 1
        print("note: jsonschema not installed, schema layer skipped (semantic rules still checked)", file=sys.stderr)

    failed = False
    for path in paths:
        errors = []
        try:
            fx = json.loads(path.read_text())
        except (OSError, ValueError) as e:
            errors.append(str(e))
            fx = None
        if fx is not None:
            if validator is not None:
                for e in sorted(validator.iter_errors(fx), key=lambda e: list(e.absolute_path)):
                    where = "/".join(str(x) for x in e.absolute_path) or "(root)"
                    errors.append(f"{where}: {e.message}")
            if isinstance(fx, dict):
                errors.extend(semantic_errors(fx))
                if path.parent.name != fx.get("name"):
                    errors.append(f"name {fx.get('name')!r} does not match directory {path.parent.name!r}")
        rel = path.relative_to(ROOT) if path.is_relative_to(ROOT) else path
        if errors:
            failed = True
            print(f"FAIL {rel}")
            for e in errors:
                print(f"     {e}")
        else:
            print(f"ok   {rel}")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
