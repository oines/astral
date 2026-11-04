#!/usr/bin/env python3
"""Run an offline lifecycle or warm Host success baseline per fresh process/database.

Build the selected harness test binary separately. Use --suite host --scenario
text/tool/compact, or lifecycle control/captured (captured needs fixture/target).
This runner records source
identity but does not attest which sources a separate compiler used. Run the
same script and target-native binary on another machine after explicit copying.
No builds, installations, SSH, database queries or production access are performed.
"""

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import statistics
import subprocess
import sys
import time


def sha(path):
    digest = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def write(path, value):
    with Path(path).open("x", encoding="utf-8") as stream:
        json.dump(value, stream, indent=2, ensure_ascii=False)
        stream.write("\n")


def command(args, cwd=None):
    return subprocess.check_output(args, cwd=cwd, text=True).strip()


def source_identity(directory):
    directory = Path(directory).resolve(strict=True)
    def git(*args):
        return subprocess.check_output(["git", "-C", str(directory), *args])
    patch = git("diff", "--binary", "HEAD", "--")
    untracked = {}
    for name in git("ls-files", "--others", "--exclude-standard", "-z").split(b"\0"):
        if name:
            relative = os.fsdecode(name)
            path = directory / relative
            if path.is_symlink() or not path.is_file():
                raise ValueError("untracked source must be a regular file: " + relative)
            untracked[relative] = sha(path)
    return {"directory": str(directory), "commit": git("rev-parse", "HEAD").decode().strip(),
            "tree": git("rev-parse", "HEAD^{tree}").decode().strip(),
            "dirty": bool(git("status", "--porcelain")),
            "tracked_patch_sha256": hashlib.sha256(patch).hexdigest(),
            "untracked_sha256": untracked}


def hardware():
    result = {"system": platform.system(), "release": platform.release(),
              "machine": platform.machine(), "hostname": platform.node(),
              "logical_cpus": os.cpu_count(), "python": platform.python_version()}
    if sys.platform == "darwin":
        for key in ("hw.model", "hw.memsize", "machdep.cpu.brand_string"):
            completed = subprocess.run(["sysctl", "-n", key], capture_output=True, text=True)
            if completed.returncode == 0:
                result[key] = completed.stdout.strip()
    elif sys.platform.startswith("linux"):
        for name in ("/proc/cpuinfo", "/proc/meminfo"):
            if Path(name).exists():
                lines = Path(name).read_text().splitlines()
                result[Path(name).name] = [line for line in lines if
                    line.startswith(("model name", "Hardware", "MemTotal"))][:1]
    return result


def reject_production(path):
    path = Path(path).resolve()
    home = Path.home()
    for root in (home / "Library/Application Support/astral", home / ".local/share/astral", Path("/var/lib/astral")):
        if path == root or root in path.parents:
            raise ValueError("production path is forbidden: " + str(path))
    return path


def private_regular(path):
    path = Path(path)
    if path.is_symlink() or not path.is_file():
        raise ValueError("regular, non-symlink input required: " + str(path))
    return reject_production(path).resolve(strict=True)


def file_identity(path):
    path = private_regular(path)
    return {"bytes": path.stat().st_size, "sha256": sha(path)}


def build_identity(args):
    value = {"binary": file_identity(args.binary), "runner_sha256": sha(__file__),
             "go_build_info": command(["go", "version", "-m", str(args.binary.resolve())]),
             "identity_note": "source identity recorded by runner; binary hash and Go build info identify the separately compiled artifact"}
    if args.source_identity:
        recorded = json.loads(private_regular(args.source_identity).read_text())
        recorded = recorded.get("build", recorded)
        if "binary" in recorded and recorded["binary"] != value["binary"]:
            raise ValueError("portable source identity belongs to a different binary")
        for name in ("host_source", "core_source"):
            if name in recorded:
                value[name] = recorded[name]
        value["source_identity_file"] = file_identity(args.source_identity)
    for name in ("host_source", "core_source"):
        directory = getattr(args, name)
        if directory:
            value[name] = source_identity(directory)
    return value


def inputs(args):
    paths = {}
    if args.scenario == "captured":
        if not args.fixture_dir or not args.target:
            raise ValueError("captured scenario requires --fixture-dir and --target")
        directory = reject_production(args.fixture_dir).resolve(strict=True)
        if args.fixture_dir.is_symlink() or not directory.is_dir() or directory.stat().st_mode & 0o077:
            raise ValueError("fixture directory must be private (0700), with no symlink")
        fixture = private_regular(directory / "fixture.json")
        manifest = json.loads(fixture.read_text())
        if not manifest.get("target_session_id"):
            raise ValueError("sealed fixture has no target_session_id")
        files = manifest.get("files", {})
        if "core.db" not in files:
            raise ValueError("sealed fixture must include core.db")
        for name, expected in files.items():
            if Path(name).name != name or name in (".", ".."):
                raise ValueError("invalid sealed fixture filename")
            path = directory / name
            # A checkpoint may remove an empty WAL. Only this exact sealed
            # empty file may be absent; never recreate anything in the source.
            if (name == "core.db-wal" and not path.exists() and not path.is_symlink()
                    and expected.get("bytes") == 0
                    and expected.get("sha256") == hashlib.sha256(b"").hexdigest()):
                continue
            path = private_regular(path)
            if file_identity(path) != {"bytes": expected["bytes"], "sha256": expected["sha256"]}:
                raise ValueError("sealed fixture file mismatch: " + name)
            paths["fixture/" + name] = path
        wal = directory / "core.db-wal"
        if wal.exists() and wal.stat().st_size:
            raise ValueError("sealed fixture has a nonempty WAL")
        paths["fixture/fixture.json"] = fixture
        paths["inputs/target.json"] = private_regular(args.target)
    for name in ("providers", "connection"):
        path = getattr(args, name)
        if path:
            paths["inputs/" + name + ".json"] = private_regular(path)
    return paths


def run_child(argv, env, log, timeout):
    """wait4 is per-child: ru_maxrss is not the cumulative maximum of prior runs."""
    started = time.monotonic_ns()
    timed_out = False
    with log.open("xb") as output:
        child = subprocess.Popen(argv, env=env, stdout=output, stderr=subprocess.STDOUT,
                                 start_new_session=True)
        killed_at = None
        while True:
            pid, status, usage = os.wait4(child.pid, os.WNOHANG)
            if pid:
                child.returncode = os.waitstatus_to_exitcode(status)
                break
            elapsed = (time.monotonic_ns() - started) / 1e9
            if elapsed > timeout and not timed_out:
                timed_out = True
                killed_at = time.monotonic()
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
            elif timed_out and time.monotonic() - killed_at > 5:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
            time.sleep(0.025)
    rss = usage.ru_maxrss if sys.platform == "darwin" else usage.ru_maxrss * 1024
    return {"exit_code": child.returncode, "timed_out": timed_out,
            "wall_ns": time.monotonic_ns() - started,
            "cpu_ns": round((usage.ru_utime + usage.ru_stime) * 1e9),
            "peak_rss_bytes": rss, "peak_rss_scope": "entire isolated test process, including setup and teardown",
            "peak_rss_method": "wait4 RUSAGE_CHILD per process; Darwin bytes, Linux KiB converted to bytes"}


def aggregate(rounds):
    metrics = {}
    for scope in ("process", "measurement"):
        keys = set().union(*(row.get(scope, {}).keys() for row in rounds)) if rounds else set()
        for key in sorted(keys):
            values = [row[scope][key] for row in rounds if
                      isinstance(row.get(scope, {}).get(key), (int, float)) and
                      not isinstance(row[scope][key], bool)]
            if len(values) == len(rounds) and values:
                metrics[scope + "." + key] = {"count": len(values), "median": statistics.median(values),
                    "min": min(values), "max": max(values), "values": values}
    return {"rounds": len(rounds), "statistics": "median and observed range; no inferred p95/p99",
            "metrics": metrics}


def validate_result(args, measurement, metadata, build, file_ids):
    scenario_key = "mode" if args.suite == "host" else "scenario"
    if measurement.get("success") is not True or measurement.get(scenario_key) != args.scenario:
        raise ValueError("test did not explicitly complete the selected success scenario")
    if args.suite == "lifecycle" and metadata.get("binary_sha256") != build["binary"]["sha256"]:
        raise ValueError("test metadata binary identity mismatch")
    for key in ("wall_ns", "cpu_ns", "total_alloc_bytes"):
        if not isinstance(measurement.get(key), int) or measurement[key] < 0:
            raise ValueError("missing/invalid measurement: " + key)
    if args.suite == "host":
        requests = measurement.get("requests", [])
        expected = 2 if args.scenario in ("tool", "compact") else 1
        if len(requests) != expected or measurement.get("http_calls") != expected:
            raise ValueError("Host result has an unexpected successful request count")
        for request in requests:
            digest = request.get("sha256", "")
            if not isinstance(request.get("bytes"), int) or request["bytes"] < 1 or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
                raise ValueError("Host result lacks complete request byte/hash identity")
    elif args.scenario == "captured":
        for field, name in (("fixture_manifest_sha256", "fixture/fixture.json"),
                            ("source_core_db_sha256", "fixture/core.db")):
            if metadata.get(field) != file_ids[name]["sha256"]:
                raise ValueError("test metadata input identity mismatch: " + field)
    if measurement.get("forced_gc_during_measurement", measurement.get("forced_gc", 0)) != 0:
        raise ValueError("forced GC invalidates the requested measurement")


def local_run(args):
    build = build_identity(args)
    paths = inputs(args)
    file_ids = {name: file_identity(path) for name, path in paths.items()}
    output = reject_production(args.output)
    if args.fixture_dir and (output == args.fixture_dir.resolve() or args.fixture_dir.resolve() in output.parents):
        raise ValueError("results must be outside original fixture")
    output.mkdir(mode=0o700)
    test_name = "TestHostSuccessBaseline" if args.suite == "host" else "TestSuccessLifecycleReplay"
    profiles_enabled = args.profile or any(os.environ.get(name) == "1" for name in
        ("ASTRAL_SUCCESS_PROFILE", "ASTRAL_SUCCESS_REPLAY_PROFILES"))
    require_anchor = os.environ.get("ASTRAL_SUCCESS_REPLAY_REQUIRE_ANCHOR") == "1"
    if require_anchor and (args.suite != "lifecycle" or args.scenario != "captured"):
        raise ValueError("strict usage anchor requires lifecycle captured")
    if args.receiver == "process" and not require_anchor:
        raise ValueError("process lifecycle receiver requires strict captured anchor mode")
    identity = {"schema": "astral-success-run-v1", "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "build": build, "runtime_hardware": hardware(), "inputs": file_ids, "suite": args.suite, "scenario": args.scenario,
        "head_override": args.head, "source_attempt_id": args.attempt, "test": test_name, "rounds_requested": args.rounds,
        "scope": "warm synthetic Host Submit lifecycle; not a captured large-context baseline" if args.suite == "host" else "Core+SDK success lifecycle; see test metadata for Host/environment omissions",
        "fixture": {"kind": "synthetic, regenerated by test each round", "identity": "selected test source in declared Host source identity", "wire_hashes": "per-request in each raw result; IDs and timestamps may change across rounds"} if args.suite == "host" else {"kind": args.scenario},
        "profile_enabled": profiles_enabled, "receiver": args.receiver, "require_historical_usage_anchor": require_anchor,
        "runtime_environment": {name: os.environ.get(name, "unset") for name in ("GOGC", "GOMEMLIMIT", "GOMAXPROCS", "GODEBUG")},
        "sampling": {"rss": "per-process wait4 high-water mark, no sampler subprocesses",
                     "heap": "before/after MemStats and post-window heap profile; no sampled heap peak" if args.suite == "host" else "see raw lifecycle result/metadata for measurement-window heap sampling"},
        "no_forced_gc_by_runner": True, "no_build_or_install_by_runner": True}
    write(output / "identity.json", identity)
    rounds = []
    try:
        for number in range(1, args.rounds + 1):
            if file_identity(args.binary) != build["binary"]:
                raise ValueError("binary changed between rounds")
            if {name: file_identity(path) for name, path in paths.items()} != file_ids:
                raise ValueError("sealed inputs changed between rounds")
            root = output / ("round-%03d" % number)
            root.mkdir(mode=0o700)
            case = root / "case"
            case.mkdir(mode=0o700)
            if args.scenario == "captured":
                source = paths["fixture/core.db"]
                target = case / "core.db"
                if sys.platform == "darwin":
                    subprocess.run(["/bin/cp", "-cp", str(source), str(target)], check=True)
                else:
                    shutil.copyfile(source, target)
                target.chmod(0o600)
                if file_identity(target) != file_ids["fixture/core.db"]:
                    raise ValueError("fresh disposable database copy mismatch")
            env = {key: value for key, value in os.environ.items()
                   if not key.startswith(("ASTRAL_HOST_REPLAY_", "ASTRAL_HOST_SUCCESS_", "ASTRAL_SUCCESS_REPLAY"))}
            if args.suite == "host":
                env.update({"ASTRAL_HOST_SUCCESS_MODE": args.scenario, "ASTRAL_HOST_SUCCESS_OUTPUT": str(root / "measurement")})
            else:
                env.update({"ASTRAL_SUCCESS_REPLAY": args.scenario, "ASTRAL_HOST_REPLAY_CASE": str(case),
                            "ASTRAL_HOST_REPLAY_OUTPUT": str(root / "measurement"),
                            "ASTRAL_SUCCESS_REPLAY_RECEIVER": args.receiver})
            if profiles_enabled:
                env.update({"ASTRAL_SUCCESS_PROFILE": "1", "ASTRAL_SUCCESS_REPLAY_PROFILES": "1"})
            if require_anchor:
                env["ASTRAL_SUCCESS_REPLAY_REQUIRE_ANCHOR"] = "1"
            if args.scenario == "captured":
                env.update({"ASTRAL_HOST_REPLAY_FIXTURE": str(paths["fixture/fixture.json"]),
                            "ASTRAL_HOST_REPLAY_TARGET": str(paths["inputs/target.json"]),
                            "ASTRAL_HOST_REPLAY_METADATA": str(args.fixture_dir.resolve() / "host-metadata.json")})
            for name in ("providers", "connection"):
                if getattr(args, name):
                    env["ASTRAL_HOST_REPLAY_" + name.upper()] = str(getattr(args, name).resolve())
            if args.head:
                env["ASTRAL_SUCCESS_REPLAY_HEAD"] = args.head
            if args.attempt:
                env["ASTRAL_SUCCESS_REPLAY_ATTEMPT"] = args.attempt
            argv = [str(args.binary.resolve()), "-test.run", "^" + test_name + "$",
                    "-test.count=1", "-test.timeout=%ss" % args.timeout, "-test.v"]
            process = run_child(argv, env, root / "test.log", args.timeout + 10)
            write(root / "process.json", process)
            if process["exit_code"] or process["timed_out"]:
                raise ValueError("round %d failed; inspect %s" % (number, root / "test.log"))
            measurement = json.loads((root / "measurement/result.json").read_text())
            metadata = {} if args.suite == "host" else json.loads((root / "measurement/metadata.json").read_text())
            validate_result(args, measurement, metadata, build, file_ids)
            if require_anchor and not (measurement.get("historical_usage_anchor_verified") and measurement.get("new_reply_usage_omitted") and metadata.get("historical_usage_anchor", {}).get("anchor_hit_verified")):
                raise ValueError("strict original usage anchor was not verified by this binary")
            if profiles_enabled:
                for name in ("cpu.pprof", "heap.pprof"):
                    path = root / "measurement" / name
                    if not path.is_file() or path.stat().st_size == 0:
                        raise ValueError("requested profile was not generated: " + name)
            rounds.append({"round": number, "process": process, "measurement": measurement})
            write(root / "validated.json", {"success": True, "binary_and_inputs_verified": True})
            print(json.dumps({"round": number, "wall_ns": measurement["wall_ns"],
                              "peak_rss_bytes": process["peak_rss_bytes"]}), flush=True)
        if file_identity(args.binary) != build["binary"] or {name: file_identity(path) for name, path in paths.items()} != file_ids:
            raise ValueError("binary or sealed inputs changed during execution")
        for name in ("host_source", "core_source"):
            if getattr(args, name) and source_identity(build[name]["directory"]) != build[name]:
                raise ValueError(name + " changed during execution; results cannot identify one source state")
    except BaseException as error:
        write(output / "failure.json", {"error": str(error), "validated_rounds": len(rounds)})
        write(output / "summary.json", {"success": False, **aggregate(rounds)})
        raise
    summary = {"success": True, **aggregate(rounds)}
    write(output / "summary.json", summary)
    print(json.dumps({"output": str(output), "success": True, "rounds": len(rounds)}))


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--host-source", type=Path, help="declared Host source tree used for this binary")
    parser.add_argument("--core-source", type=Path, help="declared Core source tree used for this binary")
    parser.add_argument("--source-identity", type=Path, help="optional prior identity.json for the exact same binary on another machine")
    parser.add_argument("--suite", choices=("lifecycle", "host"), default="lifecycle")
    parser.add_argument("--scenario", "--mode", choices=("control", "captured", "text", "tool", "compact"), default="captured")
    parser.add_argument("--profile", action="store_true", help="enable test-owned CPU profile; record its overhead")
    parser.add_argument("--receiver", choices=("full", "fast", "process"), default="full", help="offline receiver; process isolates strict captured model validation, full preserves baseline")
    parser.add_argument("--fixture-dir", type=Path)
    parser.add_argument("--target", type=Path)
    parser.add_argument("--providers", type=Path)
    parser.add_argument("--connection", type=Path)
    parser.add_argument("--head", help="explicit captured immutable fork head, if selected by the test")
    parser.add_argument("--attempt", help="captured successful source attempt whose frozen target the test verifies")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--rounds", type=int, default=3)
    parser.add_argument("--timeout", type=int, default=300)
    args = parser.parse_args()
    if not args.output or not 1 <= args.rounds <= 100 or not 1 <= args.timeout <= 3600:
        parser.error("new --output, 1..100 rounds and 1..3600 timeout seconds required")
    if args.scenario not in (("text", "tool", "compact") if args.suite == "host" else ("control", "captured")):
        parser.error("host suite requires text/tool/compact; lifecycle suite requires control/captured")
    if args.suite == "host" and (args.fixture_dir or args.target or args.head or args.attempt):
        parser.error("Host suite uses synthetic input, without fixture/target/head/attempt overrides")
    local_run(args)


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print("success baseline refused/failed: " + str(error), file=sys.stderr)
        sys.exit(1)
