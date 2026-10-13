#!/usr/bin/env python3
"""Fetch immutable corpus commits without changing existing checkouts."""
import argparse
import json
from pathlib import Path
import subprocess
from corpus_support import fetch_checkout, load_manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=Path(__file__).with_name("corpus.json"))
    parser.add_argument("--dest", type=Path, required=True)
    parser.add_argument("--case", action="append", default=[])
    parser.add_argument("--local-source", action="append", default=[], help="NAME=PATH to fetch an existing local repository offline")
    args = parser.parse_args()
    try:
        cases = load_manifest(args.manifest, args.case)
        sources = dict(value.split("=", 1) for value in args.local_source)
        if set(sources) - {case["name"] for case in cases}:
            parser.error("local-source name is not selected in the manifest")
    except (ValueError, OSError) as error:
        parser.error(str(error))
    reports = []
    for case in cases:
        try:
            checkout = fetch_checkout(case, args.dest, sources.get(case["name"]))
            reports.append({"name": case["name"], "commit": case["commit"], "checkout": str(checkout), "status": "fetched"})
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
            reports.append({"name": case["name"], "status": "failed", "error": str(error)})
    print(json.dumps(reports, indent=2))
    return 0 if all(report["status"] == "fetched" for report in reports) else 1


if __name__ == "__main__":
    raise SystemExit(main())
