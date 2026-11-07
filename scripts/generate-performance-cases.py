#!/usr/bin/env python3
"""Generate the offline CPU/memory/I/O scenario matrix, without fixture blobs."""

import argparse
import json
from pathlib import Path


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--seed", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--suite", choices=("common", "recovery", "probe", "longrun"), required=True)
    args = parser.parse_args()
    common = ["before", "v31", "current"]
    recovery = ["v31", "current"]
    cases = []

    def add(name, test, env, labels, rounds=3, timeout=300):
        cases.append({"name": name, "test": "^" + test + "$", "env": env,
                      "labels": labels, "rounds": rounds, "timeout": timeout})

    if args.suite == "common":
        for mode in ("text", "tool", "compact"):
            add("warm-" + mode, "TestHostSuccessBaseline",
                {"ASTRAL_HOST_SUCCESS_MODE": mode, "ASTRAL_HOST_SUCCESS_OUTPUT": "{output}"}, common)
        for scale, mode, rounds, delay in (("500k", "prompt", 20, 0), ("900k", "prompt", 20, 0),
                                            ("900k", "tool", 1, 0), ("900k", "prompt", 1, 10000)):
            name = scale + "-" + mode + ("-held" if delay else "")
            add(name, "TestHostSuccessScaledContext",
                {"ASTRAL_SUCCESS_SCALED_HOST": scale, "ASTRAL_SUCCESS_SCALED_HOST_MODE": mode,
                 "ASTRAL_SUCCESS_SCALED_HOST_ROUNDS": rounds, "ASTRAL_SUCCESS_SCALED_HOST_TEXT_STYLE": "mixed",
                 "ASTRAL_SUCCESS_SCALED_RECEIVER": "process", "ASTRAL_SUCCESS_SCALED_SEED": str(args.seed.resolve()),
                 "ASTRAL_SUCCESS_SCALED_HOST_OUTPUT": "{output}", "ASTRAL_SCALED_HOST_DELAY_MS": delay},
                common, rounds=1 if rounds == 20 else 3)
        for name, sessions, completed, children in (("idle-empty", 0, 0, 0), ("idle-history", 0, 10000, 0),
                                                    ("active-10", 10, 1000, 0), ("active-50", 50, 1000, 0),
                                                    ("settled-children-35", 0, 1000, 35)):
            add(name, "TestModelFallbackIdlePerformance",
                {"ASTRAL_FALLBACK_IDLE": 1, "ASTRAL_FALLBACK_IDLE_SECONDS": 10,
                 "ASTRAL_FALLBACK_IDLE_SESSIONS": sessions, "ASTRAL_FALLBACK_IDLE_COMPLETE": completed,
                 "ASTRAL_FALLBACK_IDLE_CHILDREN": children}, common)
    elif args.suite == "recovery":
        for name, test in (
            ("network-five-attempts", "TestModelFallbackWaitsForFiveFrozenNetworkAttemptsBeforeNextCandidate"),
            ("quota-then-overflow", "TestModelFallbackContinuesQuotaThenOverflowWithoutSameModelRepair"),
            ("compact-backup-summary", "TestModelFallbackAutomaticCompactionFailureRetriesSummaryOnNextModel"),
            ("compact-backup-large", "TestModelFallbackAutomaticCompactionFailureSkipsSummaryForLargerModel"),
            ("candidate-local-budget", "TestModelFallbackSkipsCandidateWhoseLocalContextBudgetCannotFit"),
            ("restore-after-restart", "TestModelFallbackWaitingSurvivesRestartAndProbeRecoveryReusesToolResult"),
            ("completion-cancels-probe", "TestModelFallbackNaturalCompletionCancelsInflightRecoveryProbe"),
            ("stale-probe-result", "TestModelFallbackProbeRejectsChangedModelRevision"),
            ("stopping-maintenance", "TestCloseCancelsMaintenanceBeforeJoiningFallbackWorkerBlockedOnAdmission"),
            ("subagent-inheritance", "TestSubagentInheritsParentActiveFallbackAsItsOwnPreference"),
            ("queued-subagent-unavailable", "TestQueuedSubagentUsesBotFallbackAfterItsPreferredModelIsDisabled"),
            ("im-wait-blocks-probe", "TestIMFallbackWaitKeepsDirectReplyWatchAndBlocksProbeRecovery")):
            add(name, test, {}, recovery)
        add("repeated-100", "TestModelFallbackRepeatedPerformance",
            {"ASTRAL_FALLBACK_PERF": 1, "ASTRAL_FALLBACK_PERF_STEPS": 100}, recovery)
        for sessions, seconds in ((1, 10), (10, 10), (50, 10), (1, 65)):
            add("waiting-%d-%ds" % (sessions, seconds), "TestModelFallbackWaitingPerformance",
                {"ASTRAL_FALLBACK_WAIT": 1, "ASTRAL_FALLBACK_WAIT_SESSIONS": sessions,
                 "ASTRAL_FALLBACK_WAIT_SECONDS": seconds}, recovery, rounds=1 if seconds == 65 else 3)
    elif args.suite == "probe":
        for mode in ("success", "model_failure"):
            for selection in ("default", "session", "override"):
                add("probe-" + mode + "-" + selection, "TestProbePerformance",
                    {"ASTRAL_PROBE_PERFORMANCE": 1, "ASTRAL_PROBE_PERFORMANCE_STEPS": 100,
                     "ASTRAL_PROBE_PERFORMANCE_MODE": mode, "ASTRAL_PROBE_PERFORMANCE_SELECTION": selection},
                    ["v31_probe", "current_probe"])
    else:
        add("core-memory-1000", "TestSQLiteLongRunMemoryReplay",
            {"ASTRAL_MEMORY_REPLAY": 1, "ASTRAL_MEMORY_OUTPUT": "{output}"}, recovery,
            rounds=1, timeout=600)
    with args.output.open("x", encoding="utf-8") as output:
        json.dump(cases, output, indent=2)
        output.write("\n")
    print(json.dumps({"cases": len(cases), "output": str(args.output.resolve())}))


if __name__ == "__main__":
    main()
