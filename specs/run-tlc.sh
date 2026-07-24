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
    "SubmodulePointer|SubmodulePointer_fixed.cfg|pass"
    "SubmodulePointer|SubmodulePointer_buggy.cfg|fail"
    "TaskStatus|TaskStatus_fixed.cfg|pass"
    "TaskStatus|TaskStatus_buggy.cfg|fail"
    "ClaimRace|ClaimRace_fixed.cfg|pass"
    "ClaimRace|ClaimRace_buggy.cfg|fail"
    "EditorSessionNamespace|EditorSessionNamespace_fixed.cfg|pass"
    "EditorSessionNamespace|EditorSessionNamespace_buggy_namespace.cfg|fail"
    "EditorSessionNamespace|EditorSessionNamespace_buggy_liveguard.cfg|fail"
    "EditorSessionNamespace|EditorSessionNamespace_buggy_remote.cfg|fail"
)

rc=0
for c in "${CASES[@]}"; do
    IFS='|' read -r mod cfg expect <<< "$c"
    out="$(cd "$HERE" && java -cp "$JAR" tlc2.TLC -config "$cfg" "$mod.tla" 2>&1)"
    if echo "$out" | grep -q "No error has been found"; then
        got=pass
    elif echo "$out" | grep -q "is violated"; then
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
