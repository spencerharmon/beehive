#!/usr/bin/env bash
# Run every beehive TLA+ protocol spec against its configurations with TLC.
#
# Each spec ships a *_fixed.cfg (the idealized protocol -- MUST report no error)
# and one or more bug cfgs (each MUST reproduce a known historical defect as an
# invariant violation, with a counterexample trace). This script asserts that
# contract: fixed cfgs are expected to pass, bug cfgs are expected to fail. A
# spec that does not behave as declared exits non-zero so CI catches spec rot.
#
# TLC jar: set TLA2TOOLS to override; defaults to the common install path.
set -u

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JAR="${TLA2TOOLS:-$HOME/.local/share/tla2tools.jar}"

if [ ! -f "$JAR" ]; then
    echo "tla2tools.jar not found at $JAR (set TLA2TOOLS=/path/to/tla2tools.jar)" >&2
    echo "download: https://github.com/tlaplus/tlaplus/releases" >&2
    exit 2
fi

# module | cfg | expect(pass|fail)
CASES=(
    "MainConvergence|MainConvergence_fixed.cfg|pass"
    "MainConvergence|MainConvergence_buggy.cfg|fail"
    "MainConvergence|MainConvergence_forcerewind.cfg|fail"
    "MainConvergence|MainConvergence_stagedheal_fixed.cfg|pass"
    "MainConvergence|MainConvergence_stagedheal_buggy.cfg|fail"
    # MainConvergence conflict sub-model: swarm.go resolveConflict hands the
    # agent conflicted files; a gitlink (mode 160000) conflict cannot be
    # auto-merged. Fixed resolves to the UNION within the runner's attempt cap;
    # the buggy cfgs reproduce a non-terminating resolution loop and a byzantine
    # dropped-side merge (conflict).
    "MainConvergence|MainConvergence_conflict_fixed.cfg|pass"
    "MainConvergence|MainConvergence_conflictloop_buggy.cfg|fail"
    "MainConvergence|MainConvergence_conflictloss_buggy.cfg|fail"
    "SubmodulePointer|SubmodulePointer_fixed.cfg|pass"
    "SubmodulePointer|SubmodulePointer_buggy.cfg|fail"
    "TaskStatus|TaskStatus_fixed.cfg|pass"
    "TaskStatus|TaskStatus_buggy.cfg|fail"
    "TaskStatus|TaskStatus_buggy_check.cfg|fail"
    "TaskStatus|TaskStatus_leak_fixed.cfg|pass"
    "TaskStatus|TaskStatus_leak_buggy.cfg|fail"
    "TaskStatus|TaskStatus_resolveloop_fixed.cfg|pass"
    "TaskStatus|TaskStatus_resolveloop_buggy.cfg|fail"
    "DependencyReadiness|DependencyReadiness_fixed.cfg|pass"
    "DependencyReadiness|DependencyReadiness_buggy.cfg|fail"
    "DependencyReadiness|DependencyReadiness_cycle_buggy.cfg|fail"
    # DependencyReadiness WRITE-TIME half (writetime / acyclic): AddDep's cycle
    # check and LinkSubmodules' reciprocal write. Fixed keeps the persisted dep
    # graph acyclic (NoCycleWritten) and every link bidirectional
    # (ReciprocalLinks); buggy drops the cycle check / writes a link one direction
    # at a time and reproduces a written-in cycle or a non-reciprocal link.
    "DependencyReadiness|DependencyReadiness_writetime_fixed.cfg|pass"
    "DependencyReadiness|DependencyReadiness_writetime_buggy.cfg|fail"
    "ClaimRace|ClaimRace_fixed.cfg|pass"
    "ClaimRace|ClaimRace_buggy.cfg|fail"
    "ClaimGC|ClaimGC_fixed.cfg|pass"
    "ClaimGC|ClaimGC_buggy.cfg|fail"
    "EditorSessionNamespace|EditorSessionNamespace_fixed.cfg|pass"
    "EditorSessionNamespace|EditorSessionNamespace_buggy_namespace.cfg|fail"
    "EditorSessionNamespace|EditorSessionNamespace_buggy_liveguard.cfg|fail"
    "EditorSessionNamespace|EditorSessionNamespace_buggy_remote.cfg|fail"
    "Selection|Selection_fixed.cfg|pass"
    "Selection|Selection_starvation_buggy.cfg|fail"
    "Selection|Selection_premature_buggy.cfg|fail"
    # CrossLayer: the capstone composition of TaskStatus's false-DONE gates with
    # DependencyReadiness's edge -- a false-DONE dep prematurely unlocks its
    # dependent (crosslayer / composed / false-done / premature-unlock).
    "CrossLayer|CrossLayer_fixed.cfg|pass"
    "CrossLayer|CrossLayer_buggy.cfg|fail"
    "CrossLayer|CrossLayer_buggy_check.cfg|fail"
    "PlanConvergence|PlanConvergence_fixed.cfg|pass"
    "PlanConvergence|PlanConvergence_stale_overwrite_buggy.cfg|fail"
    "PlanConvergence|PlanConvergence_dedup_buggy.cfg|fail"
    "DeferBound|DeferBound_fixed.cfg|pass"
    "DeferBound|DeferBound_buggy.cfg|fail"
    "BootstrapRace|BootstrapRace_fixed.cfg|pass"
    "BootstrapRace|BootstrapRace_buggy.cfg|fail"
    # TaskRemovalRace: a work pass races a concurrent task/PLAN removal (reconcile
    # or operator). Fixed re-checks presence before publish and exits cleanly on a
    # vanished task; the buggy cfgs reproduce an orphan write and a non-terminating
    # lost-work loop (removal / vanish).
    "TaskRemovalRace|TaskRemovalRace_fixed.cfg|pass"
    "TaskRemovalRace|TaskRemovalRace_orphan_buggy.cfg|fail"
    "TaskRemovalRace|TaskRemovalRace_loop_buggy.cfg|fail"
)

rc=0
i=0
for c in "${CASES[@]}"; do
    IFS='|' read -r mod cfg expect <<< "$c"
    # Unique metadir per run: TLC otherwise names its states dir from the current
    # time and back-to-back sub-second runs collide ("writes its files to a
    # directory whose name is generated from the current time").
    i=$((i + 1))
    md="$(mktemp -d "${TMPDIR:-/tmp}/tlc-states.XXXXXX")"
    out="$(cd "$HERE" && java -cp "$JAR" tlc2.TLC -metadir "$md" -config "$cfg" "$mod.tla" 2>&1)"
    rm -rf "$md"
    if echo "$out" | grep -q "No error has been found"; then
        got=pass
    elif echo "$out" | grep -qE "is violated|Temporal properties were violated"; then
        got=fail
    else
        got=error
    fi
    if [ "$got" = "$expect" ]; then
        echo "OK   $cfg (expected $expect)"
    else
        echo "FAIL $cfg (expected $expect, got $got)"
        echo "$out" | tail -20
        rc=1
    fi
done

exit $rc
