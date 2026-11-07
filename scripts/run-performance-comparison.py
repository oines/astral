#!/usr/bin/env python3
"""Run identical offline Go test scenarios in isolated processes, serially.

The JSON case list names exact tests and environment values. {output} expands
to a new per-run measurement directory. No compiler, production data, installer
or external model is invoked by this runner. Build each binary separately.
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import statistics
import sys

sys.dont_write_bytecode = True


spec = importlib.util.spec_from_file_location(
    "success_baseline", Path(__file__).with_name("run-success-baseline.py"))
baseline = importlib.util.module_from_spec(spec)
spec.loader.exec_module(baseline)

validator_path = Path(__file__).with_name("validate-performance-case.py")
validator_spec = importlib.util.spec_from_file_location("performance_validator", validator_path)
validator = importlib.util.module_from_spec(validator_spec)
validator_spec.loader.exec_module(validator)


def named(value):
    name, separator, path = value.partition("=")
    if not separator or not name or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-" for c in name):
        raise argparse.ArgumentTypeError("expected LABEL=/absolute/path")
    return name, Path(path).resolve(strict=True)


def numbers(value, prefix=""):
    result = {}
    if isinstance(value, dict):
        for key, child in value.items():
            path = prefix + "." + key if prefix else key
            if isinstance(child, dict):
                result.update(numbers(child, path))
            elif isinstance(child, (int, float)) and not isinstance(child, bool):
                result[path] = child
    return result


def summarize(rows):
    flattened = [numbers(row) for row in rows]
    keys = set.intersection(*(set(row) for row in flattened)) if flattened else set()
    return {key: {"median": statistics.median([row[key] for row in flattened]),
                  "min": min(row[key] for row in flattened),
                  "max": max(row[key] for row in flattened),
                  "values": [row[key] for row in flattened]}
            for key in sorted(keys)}


def scaled_seed_identity(cases):
    paths = set()
    for case in cases:
        if case["test"] != "^TestHostSuccessScaledContext$":
            continue
        raw = case.get("env", {}).get("ASTRAL_SUCCESS_SCALED_SEED")
        if not isinstance(raw, str) or not Path(raw).is_absolute():
            raise ValueError("scaled cases require an absolute immutable seed file")
        paths.add(baseline.private_regular(Path(raw)))
    if not paths:
        return None
    if len(paths) != 1:
        raise ValueError("scaled cases in one suite must share one immutable seed file")
    path = paths.pop()
    return {"path": str(path), **baseline.file_identity(path)}


def verify_seed_unchanged(identity):
    if identity is not None:
        expected = {key: identity[key] for key in ("bytes", "sha256")}
        if baseline.file_identity(Path(identity["path"])) != expected:
            raise ValueError("immutable scaled seed changed during measurement")


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", action="append", type=named, required=True)
    parser.add_argument("--source", action="append", type=named, required=True)
    parser.add_argument("--cases", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--timeout", type=int, default=300)
    args = parser.parse_args()
    if not 1 <= args.rounds <= 100 or not 1 <= args.timeout <= 3600:
        parser.error("rounds must be 1..100 and timeout 1..3600 seconds")
    binaries, sources = dict(args.binary), dict(args.source)
    if len(binaries) != len(args.binary) or len(sources) != len(args.source) or binaries.keys() != sources.keys():
        parser.error("provide one binary and source for every unique label")
    output = baseline.reject_production(args.output)
    output.mkdir(mode=0o700)
    cases = json.loads(baseline.private_regular(args.cases).read_text())
    if not isinstance(cases, list) or not cases:
        raise ValueError("nonempty JSON case list required")
    names = set()
    for case in cases:
        name = case["name"]
        if name in names or not name or any(c not in "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-" for c in name):
            raise ValueError("duplicate or unsafe case name")
        names.add(name)
        if validator.EXACT_TEST.fullmatch(case["test"]) is None:
            raise ValueError("select one anchored exact top-level test")
        if not set(case.get("labels", binaries)).issubset(binaries):
            raise ValueError("case has an unknown binary label")
    seed_identity = scaled_seed_identity(cases)
    identity = {"hardware": baseline.hardware(), "cases": cases,
                "case_file": baseline.file_identity(args.cases),
                "runner": baseline.file_identity(__file__),
                "validator": baseline.file_identity(validator_path),
                "seed": seed_identity,
                "statistics": "independent fresh processes; median and observed range, no inferred p95/p99",
                "order": "per case and round, binary labels run serially in alternating order",
                "environment": {key: os.environ.get(key, "unset") for key in ("GOGC", "GOMEMLIMIT", "GOMAXPROCS", "GODEBUG")},
                "builds": {label: {"binary": baseline.file_identity(binary),
                                     "go_build_info": baseline.command(["go", "version", "-m", str(binary)]),
                                     "source": baseline.source_identity(sources[label])}
                           for label, binary in binaries.items()}}
    baseline.write(output / "identity.json", identity)
    rows = {}
    try:
        for case in cases:
            labels = [label for label in binaries if label in case.get("labels", binaries)]
            for round_index in range(case.get("rounds", args.rounds)):
                order = labels if round_index % 2 == 0 else labels[::-1]
                for label in order:
                    binary = binaries[label]
                    if baseline.file_identity(binary) != identity["builds"][label]["binary"]:
                        raise ValueError("binary changed during measurement")
                    directory = output / case["name"] / label / ("round-%02d" % (round_index + 1))
                    directory.mkdir(parents=True, mode=0o700)
                    env = {key: value for key, value in os.environ.items() if not key.startswith("ASTRAL_")}
                    env["ASTRAL_PERFORMANCE_OUTPUT"] = str(directory / "resources")
                    for key, value in case.get("env", {}).items():
                        env[key] = str(value).replace("{output}", str(directory / "measurement"))
                    timeout = int(case.get("timeout", args.timeout))
                    process = baseline.run_child(
                        [str(binary), "-test.run", case["test"], "-test.count=1", "-test.timeout=%ds" % timeout, "-test.v"],
                        env, directory / "test.log", timeout + 10)
                    baseline.write(directory / "process.json", process)
                    if process["exit_code"] or process["timed_out"]:
                        raise ValueError("case %s/%s round %d failed: %s" % (case["name"], label, round_index + 1, directory / "test.log"))
                    resource_file = directory / "resources/performance.json"
                    row = validator.validate_case(case, directory, seed_identity["sha256"] if seed_identity else None)
                    rows.setdefault(case["name"], {}).setdefault(label, []).append(row)
                    baseline.write(directory / "validated.json", {
                        "success": True, "resource_file": baseline.file_identity(resource_file),
                        "validator": identity["validator"], "validation": row["validation"],
                        "artifact_kinds": sorted(key for key in row if key != "validation"),
                        "expected_seed_sha256": seed_identity["sha256"] if seed_identity else None,
                    })
                    print(json.dumps({"case": case["name"], "label": label, "round": round_index + 1,
                                      "wall_ns": process["wall_ns"], "cpu_ns": process["cpu_ns"],
                                      "peak_rss_bytes": process["peak_rss_bytes"]}), flush=True)
        verify_seed_unchanged(seed_identity)
        if baseline.file_identity(validator_path) != identity["validator"]:
            raise ValueError("validator changed during measurement")
        for label in binaries:
            if baseline.file_identity(binaries[label]) != identity["builds"][label]["binary"] or baseline.source_identity(sources[label]) != identity["builds"][label]["source"]:
                raise ValueError("binary/source changed during execution: " + label)
    except BaseException as error:
        baseline.write(output / "failure.json", {"error": str(error)})
        baseline.write(output / "summary.json", {"success": False, "completed": {case: {label: len(values) for label, values in labels.items()} for case, labels in rows.items()}})
        raise
    summary = {case: {label: {"rounds": len(values), "metrics": summarize(values)} for label, values in labels.items()} for case, labels in rows.items()}
    comparisons = {}
    for case, labels in summary.items():
        if len(labels) < 2:
            continue
        reference = next(iter(labels))
        for label in list(labels)[1:]:
            common = labels[reference]["metrics"].keys() & labels[label]["metrics"].keys()
            comparisons[case + ":" + reference + "->" + label] = {
                key: {"before": labels[reference]["metrics"][key]["median"],
                      "after": labels[label]["metrics"][key]["median"],
                      "percent": ((labels[label]["metrics"][key]["median"] / labels[reference]["metrics"][key]["median"] - 1) * 100)
                      if labels[reference]["metrics"][key]["median"] else None}
                for key in sorted(common)}
    baseline.write(output / "summary.json", {"success": True, "cases": summary, "comparisons": comparisons})


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError) as error:
        print("performance run failed: " + str(error), file=sys.stderr)
        sys.exit(1)
