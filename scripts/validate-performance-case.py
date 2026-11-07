#!/usr/bin/env python3
"""Validate offline performance artifacts without rerunning or changing them.

Only small logs/JSON are read. Seed bytes, databases and profiles are never read.
Source/binary stability remains the runner's responsibility. Partial acceptance
describes only completed rounds and never changes an original failed suite into
a successful suite.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys

sys.dont_write_bytecode = True

SHA256 = re.compile(r"[0-9a-f]{64}\Z")
EXACT_TEST = re.compile(r"\^(Test[A-Za-z0-9_]+)\$\Z")
MAX_ARTIFACT_BYTES = 8 << 20
VALIDATOR_VERSION = 2


def require(condition, message):
    if not condition:
        raise ValueError(message)


def read_text(path: Path) -> str:
    require(not path.is_symlink() and path.is_file(), "missing/non-regular artifact: " + str(path))
    require(path.stat().st_size <= MAX_ARTIFACT_BYTES, "artifact exceeds small-file limit: " + str(path))
    return path.read_text(encoding="utf-8")


def read_json(path: Path) -> dict:
    value = json.loads(read_text(path))
    require(isinstance(value, dict), "JSON object required: " + str(path))
    return value


def integer(value, label, minimum=0):
    require(type(value) is int and value >= minimum, "invalid " + label)
    return value


def env_integer(case, name, default):
    raw = case.get("env", {}).get(name, default)
    require(not isinstance(raw, bool), "invalid environment integer: " + name)
    try:
        value = int(raw)
    except (TypeError, ValueError) as error:
        raise ValueError("invalid environment integer: " + name) from error
    require(str(raw) == str(value), "noncanonical environment integer: " + name)
    return value


def invariants(value, label):
    fields = value.get("invariants")
    require(isinstance(fields, dict) and bool(fields), label + " has no invariants")
    failed = [name for name, passed in fields.items() if passed is not True]
    require(not failed, label + " failed invariants: " + ", ".join(failed))


def resource_window(value, label):
    require(isinstance(value, dict), label + " resource window missing")
    require(value.get("cpu_available") is True, label + " CPU unavailable")
    require(value.get("io_available") is True, label + " OS byte accounting unavailable")
    for key in ("wall_ns", "cpu_ns", "total_alloc_bytes", "disk_read_bytes", "disk_write_bytes"):
        integer(value.get(key), label + "." + key)


def hot_window(value, label):
    resource_window(value, label)
    require(value.get("forced_gc_cycles") == 0, label + " forced GC occurred in the hot window")


def metric_fields(value, label):
    for key in ("wall_ns", "cpu_ns", "total_alloc_bytes"):
        integer(value.get(key), label + "." + key)


def storage_snapshot(value, label):
    require(isinstance(value, dict), label + " storage snapshot missing")
    errors = value.get("errors", [])
    require(isinstance(errors, list) and not errors, label + " storage snapshot contains errors")
    require(value.get("operations_available") is True, label + " Host operations accounting unavailable")
    operations = value.get("operations")
    require(isinstance(operations, dict), label + " operations counters missing")
    for key in ("count", "json_bytes"):
        integer(operations.get(key), label + ".operations." + key)
    files = value.get("files")
    require(isinstance(files, dict), label + " file accounting missing")
    for name, entry in files.items():
        require(isinstance(name, str) and isinstance(entry, dict), label + " invalid file accounting")
        integer(entry.get("bytes"), label + ".files.bytes")


def wire_identity(value, label, scaled=False):
    require(isinstance(value, dict), label + " wire object missing")
    integer(value.get("bytes"), label + ".bytes", 1)
    require(isinstance(value.get("sha256"), str) and SHA256.fullmatch(value["sha256"]), label + " invalid wire SHA-256")
    if scaled:
        require(value.get("valid_json") is True and value.get("streaming") is False, label + " incomplete/non-JSON wire")
    return value["bytes"], value["sha256"]


def validate_scaled(case, directory, row, expected_seed):
    value = row["measurement"]
    metadata = read_json(directory / "measurement/metadata.json")
    row["metadata"] = metadata
    env = case.get("env", {})
    scale, mode = env.get("ASTRAL_SUCCESS_SCALED_HOST"), env.get("ASTRAL_SUCCESS_SCALED_HOST_MODE", "prompt")
    require(scale in ("500k", "900k") and mode in ("prompt", "tool"), "invalid scaled case selection")
    rounds = env_integer(case, "ASTRAL_SUCCESS_SCALED_HOST_ROUNDS", 1)
    require(1 <= rounds <= 20, "invalid scaled round count")
    require(value.get("scenario") == scale and value.get("mode") == mode, "scaled result scenario mismatch")
    require(metadata.get("scenario") == scale and metadata.get("mode") == mode, "scaled metadata scenario mismatch")
    require(value.get("round_count") == rounds and metadata.get("round_count") == rounds, "scaled round count mismatch")
    require(value.get("receiver") == env.get("ASTRAL_SUCCESS_SCALED_RECEIVER", "full"), "scaled receiver mismatch")
    require(value.get("text_style") == env.get("ASTRAL_SUCCESS_SCALED_HOST_TEXT_STYLE", "plain"), "scaled text style mismatch")
    delay_ms = env_integer(case, "ASTRAL_SCALED_HOST_DELAY_MS", 0)
    require(0 <= delay_ms <= 30000, "invalid scaled receiver delay")
    reported_delay = env_integer({"env": {"receiver_delay_ms": value.get("receiver_delay_ms")}}, "receiver_delay_ms", 0)
    require(reported_delay == delay_ms, "scaled receiver delay differs from the case")
    require(metadata.get("images") == 227, "scaled metadata must describe 227 images")
    positions = metadata.get("image_message_positions")
    require(isinstance(positions, list) and len(positions) == 227 and all(type(at) is int and at > 0 for at in positions), "scaled image positions incomplete")
    for key in ("calibrated_fixture_bytes", "image_raw_json_bytes", "history_messages"):
        integer(metadata.get(key), "scaled metadata." + key, 1)
    for key in ("seed_sha256", "calibrated_fixture_sha256"):
        require(isinstance(metadata.get(key), str) and SHA256.fullmatch(metadata[key]), "invalid scaled " + key)
    if expected_seed is not None:
        require(metadata["seed_sha256"] == expected_seed, "scaled seed differs from the expected immutable seed")
    target = {"500k": 500000, "900k": 900000}[scale]
    require(metadata.get("target_estimated_input_tokens") == target, "scaled token target mismatch")
    require(value.get("forced_gc") == 0, "scaled result reports forced GC")
    require(value.get("real_host_environment_verified") is True, "scaled real Host environment was not verified")
    if mode == "tool":
        require(value.get("real_computer_read_verified") is True, "scaled real Computer read was not verified")
    per_round = 2 if mode == "tool" else 1
    wires, verified, round_rows = value.get("wire_requests"), value.get("verified_requests"), value.get("rounds")
    require(isinstance(wires, list) and len(wires) == rounds * per_round, "scaled HTTP count mismatch")
    require(isinstance(verified, list) and len(verified) == len(wires), "scaled rebuilt request count mismatch")
    require(isinstance(round_rows, list) and len(round_rows) == rounds, "scaled per-round results missing")
    identities = [wire_identity(wire, "scaled wire %d" % (index + 1), True) for index, wire in enumerate(wires)]
    for wire in wires:
        require(wire.get("receiver_response_delay_ns", 0) == delay_ms * 1000000, "scaled wire response delay mismatch")
        require(integer(wire.get("receiver_wall_ns"), "scaled receiver wall") >= delay_ms * 1000000, "scaled receiver returned before its required hold")
    for index, entry in enumerate(verified):
        require(isinstance(entry, dict), "invalid scaled request verification")
        round_index = index // per_round
        require(entry.get("round") == round_index + 1, "scaled request/round mapping mismatch")
        require(entry.get("input_id") == round_rows[round_index].get("input_id") and bool(entry.get("input_id")), "scaled input identity mismatch")
        require(entry.get("digest_rebuild_verified") is True and entry.get("wire_rebuild_verified") is True, "scaled request digest/wire reconstruction not verified")
        require(isinstance(entry.get("digest"), str) and bool(entry["digest"]), "scaled persisted digest missing")
        require(entry.get("max_output_tokens") == 16000, "scaled output budget mismatch")
        estimate = integer(entry.get("estimated_input_tokens"), "scaled estimated input tokens", 1)
        if index == 0:
            require(abs(estimate - target) <= target // 1000, "scaled first request is outside token target tolerance")
    for index, round_row in enumerate(round_rows):
        require(isinstance(round_row, dict) and round_row.get("index") == index + 1, "scaled round identity mismatch")
        metric_fields(round_row, "scaled round")
        hot_window(round_row.get("resource_window"), "scaled round")
        round_wires = round_row.get("wire_requests")
        require(isinstance(round_wires, list) and len(round_wires) == per_round, "scaled per-round HTTP count mismatch")
        actual = [wire_identity(wire, "scaled round wire", True) for wire in round_wires]
        require(actual == identities[index * per_round:(index + 1) * per_round], "scaled per-round wire identity mismatch")
        for wire in round_wires:
            require(wire.get("receiver_response_delay_ns", 0) == delay_ms * 1000000, "scaled per-round wire response delay mismatch")
    for key in ("wall_ns", "cpu_ns", "total_alloc_bytes"):
        require(value.get(key) == sum(entry[key] for entry in round_rows), "scaled aggregate differs from per-round " + key)
    row["validation"]["seed_sha256"] = metadata["seed_sha256"]
    row["validation"]["expected_seed_verified"] = expected_seed is not None


def _validate_case(case: dict, directory: Path, expected_seed: str | None = None) -> dict:
    """Return validated raw JSON rows; raise ValueError on missing/failed evidence.

    This function never writes artifacts, loads seed contents or invokes a test.
    Keys match the comparison runner: resources, measurement, metadata, idle,
    repeat, probe, longrun and (when present) process/retention.
    """
    directory = Path(directory)
    if expected_seed is not None:
        require(isinstance(expected_seed, str) and SHA256.fullmatch(expected_seed), "expected_seed must be a lowercase SHA-256")
    match = EXACT_TEST.fullmatch(case.get("test", ""))
    require(match is not None, "validation requires one anchored exact top-level Test name")
    test = match.group(1)
    log = read_text(directory / "test.log")
    require(not re.search(r"^--- (?:SKIP|FAIL): " + re.escape(test) + r"(?:\s|$)", log, re.MULTILINE), "selected test skipped or failed: " + test)
    passed = re.findall(r"^--- PASS: " + re.escape(test) + r"(?:\s|$)", log, re.MULTILINE)
    require(len(passed) == 1, "selected top-level test must explicitly PASS exactly once: " + test)
    resource = read_json(directory / "resources/performance.json")
    require(resource.get("success") is True, "resource collector did not confirm success")
    resource_window(resource.get("difference"), "complete resource collector")
    row = {"resources": resource, "validation": {"success": True, "test": test, "top_level_pass_verified": True, "validator_version": VALIDATOR_VERSION}}
    if test != "TestProbePerformance":
        fixtures = read_json(directory / "resources/fixtures.json")
        records = fixtures.get("fixtures")
        require(isinstance(records, list), "resource fixture accounting missing")
        for index, record in enumerate(records):
            require(isinstance(record, dict), "invalid fixture accounting record")
            for endpoint in ("before", "after"):
                storage_snapshot(record.get(endpoint), "resource fixture %d %s" % (index + 1, endpoint))
        row["fixtures"] = fixtures
    process_path = directory / "process.json"
    if process_path.exists():
        process = read_json(process_path)
        require(process.get("exit_code") == 0 and process.get("timed_out") is False, "test process failed or timed out")
        row["process"] = process
    env = case.get("env", {})
    if test in ("TestHostSuccessBaseline", "TestHostSuccessScaledContext"):
        value = read_json(directory / "measurement/result.json")
        row["measurement"] = value
        require(value.get("success") is True, "Host result did not confirm success")
        metric_fields(value, "Host result")
        for endpoint in ("storage_before", "storage_after"):
            storage_snapshot(value.get(endpoint), "Host " + endpoint)
        if test == "TestHostSuccessScaledContext":
            validate_scaled(case, directory, row, expected_seed)
        else:
            mode = env.get("ASTRAL_HOST_SUCCESS_MODE", "text")
            require(mode in ("text", "tool", "compact") and value.get("mode") == mode, "Host mode mismatch")
            expected = 1 if mode == "text" else 2
            wires = value.get("requests")
            require(value.get("http_calls") == expected and isinstance(wires, list) and len(wires) == expected, "Host HTTP request count mismatch")
            require(value.get("forced_gc") == 0, "Host result reports forced GC")
            hot_window(value.get("resource_window"), "Host hot window")
            for wire in wires:
                wire_identity(wire, "Host request")
                integer(wire.get("messages"), "Host request messages", 1)
    elif test in ("TestModelFallbackIdlePerformance", "TestModelFallbackWaitingPerformance"):
        value = read_json(directory / "resources/warm-idle.json")
        row["idle"] = value
        if test == "TestModelFallbackWaitingPerformance":
            require(value.get("success") is True, "waiting result did not confirm success")
            sessions = env_integer(case, "ASTRAL_FALLBACK_WAIT_SESSIONS", 1)
            seconds = env_integer(case, "ASTRAL_FALLBACK_WAIT_SECONDS", 10)
            require(value.get("sessions") == sessions and value.get("requested_seconds") == seconds, "waiting scenario size mismatch")
            require(value.get("initial_requests_after") == sessions and value.get("probe_requests_after") == (sessions if seconds == 65 else 0), "waiting request count mismatch")
            require(value.get("total_requests") == sessions * (2 if seconds == 65 else 1), "waiting total request count mismatch")
        else:
            require("success" not in value or value["success"] is True, "idle result explicitly failed")
            for field, name, default in (("requested_waiting_sessions", "ASTRAL_FALLBACK_IDLE_SESSIONS", 0), ("completed_maintenance_records", "ASTRAL_FALLBACK_IDLE_COMPLETE", 0), ("settled_children", "ASTRAL_FALLBACK_IDLE_CHILDREN", 0), ("requested_seconds", "ASTRAL_FALLBACK_IDLE_SECONDS", 10)):
                require(value.get(field) == env_integer(case, name, default), "idle scenario size mismatch: " + field)
        invariants(value, "idle/waiting")
        hot_window(value.get("resources"), "idle/waiting hot window")
        for endpoint in ("files_before", "files_after"):
            storage_snapshot(value.get(endpoint), "idle/waiting " + endpoint)
    elif test == "TestModelFallbackRepeatedPerformance":
        value = read_json(directory / "resources/repeated.json")
        row["repeat"] = value
        steps = env_integer(case, "ASTRAL_FALLBACK_PERF_STEPS", 100)
        require(value.get("success") is True and value.get("steps") == steps, "repeated scenario did not complete")
        for key, expected in (("primary_requests", steps), ("backup_requests", 2 * steps), ("total_requests", 3 * steps), ("completed_tools", steps)):
            require(value.get(key) == expected, "repeated counter mismatch: " + key)
        samples = value.get("samples")
        require(isinstance(samples, list) and len(samples) >= 2 and samples[0].get("step") == 0 and samples[-1].get("step") == steps, "repeated endpoint samples missing")
        previous = -1
        for sample in samples:
            step = integer(sample.get("step"), "repeated sample step")
            require(previous < step <= steps, "repeated sample order mismatch")
            require(sample.get("primary_requests") == step and sample.get("backup_requests") == 2 * step, "repeated sampled request counts mismatch")
            require(sample.get("forced_gc_count") == samples[0].get("forced_gc_count"), "forced GC occurred during repeated work")
            previous = step
    elif test == "TestProbePerformance":
        value = read_json(directory / "resources/probe.json")
        row["probe"] = value
        steps = env_integer(case, "ASTRAL_PROBE_PERFORMANCE_STEPS", 100)
        mode = env.get("ASTRAL_PROBE_PERFORMANCE_MODE", "success")
        require(value.get("success") is True and value.get("steps") == steps and value.get("mode") == mode, "Probe scenario mismatch")
        require(value.get("selection_mode") == env.get("ASTRAL_PROBE_PERFORMANCE_SELECTION", "session"), "Probe selection mode mismatch")
        invariants(value, "Probe")
        hot_window(resource.get("difference"), "Probe hot window")
        require(value.get("requests") == steps and value.get("requests_after") - value.get("requests_before") == steps, "Probe request count mismatch")
        require(value.get("failed_decisions") == (steps if mode == "model_failure" else 0) and value.get("positive_decisions") == 0 and value.get("wrong_step_request_counts") == 0, "Probe decision counts mismatch")
        latencies = value.get("latency_ns", {}).get("samples")
        require(isinstance(latencies, list) and len(latencies) == steps, "Probe latency sample count mismatch")
        for latency in latencies:
            integer(latency, "Probe latency")
    elif test == "TestSQLiteLongRunMemoryReplay":
        value = read_json(directory / "measurement/natural-result.json")
        row["longrun"] = value
        require(value.get("longrun_acceptance") is True and value.get("mode") == "longrun1000", "full long-run memory acceptance did not complete")
        require(value.get("forced_gc_during_hot") == 0, "forced GC occurred during long-run memory work")
        natural = value.get("natural_end", {})
        require(natural.get("step") == 1000 and natural.get("normal_api_calls") == 2000 and natural.get("summary_api_calls") == 20, "long-run workload count mismatch")
        require(natural.get("archived_tool_results") == 1000 and natural.get("archive_attempts") == 2020, "long-run history count mismatch")
        retention = directory / "measurement/retention-diagnostic.json"
        if retention.exists():
            row["retention"] = read_json(retention)
    return row


def validate_case(case: dict, directory: Path, expected_seed: str | None = None) -> dict:
    """Validate one completed round without writing files; failures are ValueError."""
    try:
        return _validate_case(case, Path(directory), expected_seed)
    except (OSError, ValueError, TypeError, KeyError, AttributeError) as error:
        raise ValueError(str(directory) + ": " + str(error)) from error


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cases", type=Path, action="append", required=True)
    parser.add_argument("--run", type=Path, required=True)
    parser.add_argument("--expected-seed")
    parser.add_argument("--rounds", type=int, default=3, help="default when a case omits rounds")
    parser.add_argument("--output", type=Path, help="new acceptance JSON; defaults to RUN/acceptance.json")
    args = parser.parse_args()
    require(args.run.is_dir() and not args.run.is_symlink(), "run must be an existing regular directory")
    require(1 <= args.rounds <= 100, "default rounds must be 1..100")
    if args.expected_seed is not None:
        require(SHA256.fullmatch(args.expected_seed), "expected seed must be a lowercase SHA-256")
    cases = []
    for path in args.cases:
        value = json.loads(read_text(path))
        require(isinstance(value, list) and all(isinstance(case, dict) for case in value), "case list required: " + str(path))
        cases.extend(value)
    require(bool(cases), "nonempty case list required")
    identity_path = args.run / "identity.json"
    identity = read_json(identity_path) if identity_path.exists() else {}
    known_labels = list(identity.get("builds", {}))
    validated, missing, incomplete, failed = [], [], [], []
    names, seeds = set(), set()
    for case in cases:
        name = case.get("name", "")
        require(re.fullmatch(r"[A-Za-z0-9_-]+", name) and name not in names, "unsafe or duplicate case name")
        names.add(name)
        labels = case.get("labels", known_labels)
        require(isinstance(labels, list) and bool(labels) and len(set(labels)) == len(labels), "explicit unique case labels required")
        rounds = integer(case.get("rounds", args.rounds), "case rounds", 1)
        require(rounds <= 100, "case rounds exceeds 100")
        for label in labels:
            require(isinstance(label, str) and re.fullmatch(r"[A-Za-z0-9_-]+", label), "unsafe binary label")
            for index in range(1, rounds + 1):
                relative = Path(name) / label / ("round-%02d" % index)
                directory = args.run / relative
                item = {"case": name, "label": label, "round": index, "directory": relative.as_posix()}
                if not directory.exists():
                    missing.append(item)
                    continue
                try:
                    require(directory.is_dir() and not directory.is_symlink(), "round must be a regular directory")
                    process_path = directory / "process.json"
                    # The runner writes this only after wait4 completes. A PASS
                    # line alone can precede TestMain's diagnostic/output work.
                    if not process_path.exists():
                        if (directory / "test.log").is_file():
                            log = read_text(directory / "test.log")
                            require(not re.search(r"^--- (?:SKIP|FAIL): ", log, re.MULTILINE), "test log records a failure/skip")
                        incomplete.append(item)
                        continue
                    process = read_json(process_path)
                    require(process.get("exit_code") == 0 and process.get("timed_out") is False, "recorded process failure/timeout")
                    row = validate_case(case, directory, args.expected_seed)
                    if "seed_sha256" in row["validation"]:
                        seeds.add(row["validation"]["seed_sha256"])
                    validated.append({**item, "row": row})
                except (OSError, ValueError, TypeError, KeyError, AttributeError) as error:
                    failed.append({**item, "error": str(error)})
    if len(seeds) > 1:
        failed.append({"error": "scaled rounds used different seed hashes"})
    original_failure = None
    if (args.run / "failure.json").exists():
        original_failure = read_json(args.run / "failure.json")
    original_summary = read_json(args.run / "summary.json") if (args.run / "summary.json").exists() else {}
    suite_failed = original_failure is not None or original_summary.get("success") is False
    scope = "partial" if missing or incomplete else "all_expected"
    rounds_success = bool(validated) and not failed
    validator_bytes = Path(__file__).read_bytes()
    result = {"schema": "astral-performance-acceptance-v1", "success": rounds_success and not suite_failed,
              "validator_version": VALIDATOR_VERSION,
              "validator": {"name": Path(__file__).name, "bytes": len(validator_bytes), "sha256": hashlib.sha256(validator_bytes).hexdigest()},
              "scope": scope, "validated_rounds_success": rounds_success, "original_suite_failed": suite_failed,
              "original_suite_failure": original_failure, "original_suite_success": original_summary.get("success"),
              "source_stability_scope": "not checked here; retain the original runner's source/binary identity evidence",
              "expected_seed_sha256": args.expected_seed, "observed_scaled_seed_sha256": sorted(seeds),
              "validated_round_count": len(validated), "missing_round_count": len(missing),
              "incomplete_round_count": len(incomplete), "failed_round_count": len(failed),
              "validated": validated, "missing": missing, "incomplete": incomplete, "failed": failed}
    output = args.output or args.run / "acceptance.json"
    with output.open("x", encoding="utf-8") as stream:
        json.dump(result, stream, indent=2, ensure_ascii=False)
        stream.write("\n")
    print(json.dumps({key: result[key] for key in ("success", "scope", "validated_round_count", "missing_round_count", "incomplete_round_count", "failed_round_count", "original_suite_failed")}))
    if not result["success"]:
        sys.exit(1)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError, KeyError, AttributeError) as error:
        print("performance acceptance failed: " + str(error), file=sys.stderr)
        sys.exit(1)
