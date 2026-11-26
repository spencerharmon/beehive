#!/usr/bin/env python3
"""Production emitter -> deployed Alertmanager acceptance (stdlib only).

GET is verification-only. No hand-crafted POST is used, even for cleanup:
clear the fixture's conditions and run the production one-shot emitter. The
PassFailing input is a repository health fixture; preflight outcomes are produced
by three real honeybee processes. Recorder wiring is separately unit-tested.
"""
import contextlib
import datetime as dt
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request


GENERATOR = "https://beehive.example.com"
REASON = "populate key LIVE_TEST in the secret store"
PLAN = f"""# Plan
<!-- Beehive-ROI: abc -->

## live-t1 [NEEDS-HUMAN] <!-- attempts=1 deps= weight=1 category=secret -->
Live harness fixture task.
Human-needed: {REASON}
"""


def log(message):
    print(f"alertmanager-live: {message}", flush=True)


def require(ok, message):
    if not ok:
        raise AssertionError(message)


def instant(value):
    return dt.datetime.fromisoformat(value.replace("Z", "+00:00"))


def timestamp():
    return dt.datetime.now(dt.timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def get(url):
    with urllib.request.urlopen(url, timeout=10) as response:
        return response.read()


def wait_for(description, predicate, timeout=30):
    end = time.monotonic() + timeout
    while True:
        result = predicate()
        if result:
            return result
        require(time.monotonic() < end, f"timeout: {description}")
        time.sleep(1)


class Fixture:
    def __init__(self, work, name, endpoint):
        self.root = work / name
        self.root.mkdir()
        self.config = work / (name + "-config")
        self.config.mkdir()
        self.env = os.environ.copy()
        # Never inherit a runner's worktree redirects or private registry.
        for key in list(self.env):
            if key.startswith("BEEHIVE_"):
                del self.env[key]
        self.env.update(BEEHIVE_CONFIG_DIR=str(self.config),
                        GIT_AUTHOR_NAME="live-test", GIT_AUTHOR_EMAIL="live-test@example.com",
                        GIT_COMMITTER_NAME="live-test", GIT_COMMITTER_EMAIL="live-test@example.com")
        self.bins = work / "bin"
        self.name = name
        self.configure(endpoint)
        self.git("init", "-q", "-b", "main")
        self.plan = self.root / "submodules/fixture/PLAN.md"
        self.plan.parent.mkdir(parents=True)
        self.plan.write_text(PLAN)
        (self.root / "AGENTS.md").write_text("# isolated live test\n")
        (self.root / ".gitignore").write_text(".worktrees/\n.submodule-worktrees/\n")
        self.git("add", "-A")
        self.git("commit", "-qm", "fixture")
        self.health = self.root / ".git/beehive/health.json"

    def run(self, *args, success=True):
        result = subprocess.run(args, cwd=self.root, env=self.env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        require((result.returncode == 0) == success,
                f"{args} exit={result.returncode}: {result.stdout}")
        return result.stdout

    def git(self, *args):
        return self.run("git", "-C", str(self.root), *args)

    def configure(self, endpoint):
        # Exercise the supported in-repo config layer, not an emitter-only flag.
        (self.root / "config.yaml").write_text(
            "abort_on_remote_failure: true\n" +
            ("alertmanager_url: " + json.dumps(endpoint) + "\n" if endpoint else ""))

    def emit(self, success=True):
        return self.run(str(self.bins / "beehive"), "alertmanager", "emit", "--root", str(self.root),
                        "--generator-url", GENERATOR, success=success)

    def honeybee(self, success=True):
        return self.run(str(self.bins / "honeybee"), str(self.root), success=success)

    def clear(self):
        self.plan.write_text("# Plan\n<!-- Beehive-ROI: abc -->\n")
        self.git("add", "-A")
        self.git("commit", "--allow-empty", "-qm", "clear fixture conditions")
        if self.health.exists():
            self.health.write_text('{"preflight":{},"publish":{},"passes":{}}\n')

    @contextlib.contextmanager
    def daemon(self):
        # Pick a loopback ephemeral port; startup is checked and fails closed on
        # a bind race. Never attach to or signal an existing production daemon.
        with socket.socket() as sock:
            sock.bind(("127.0.0.1", 0))
            port = sock.getsockname()[1]
        path = self.config / "daemon.log"
        with path.open("w") as output:
            proc = subprocess.Popen([str(self.bins / "beehived"), "-repo", str(self.root),
                                     "-addr", f"127.0.0.1:{port}", "-external-url", GENERATOR],
                                    cwd=self.root, env=self.env, stdout=output, stderr=output)
            try:
                def ready():
                    require(proc.poll() is None, "daemon exited: " + path.read_text())
                    try:
                        return bool(get(f"http://127.0.0.1:{port}/"))
                    except urllib.error.URLError as exc:
                        if isinstance(exc.reason, ConnectionRefusedError):
                            return False
                        raise
                wait_for("fixture daemon ready", ready)
                yield path, ready
            finally:
                proc.terminate()
                try:
                    proc.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait()
                    raise AssertionError("fixture daemon did not terminate")
                log("fixture daemon log: " + path.read_text().strip())


class RejectReceiver(http.server.BaseHTTPRequestHandler):
    posts = 0

    def do_POST(self):
        self.rfile.read(int(self.headers["Content-Length"]))
        type(self).posts += 1
        self.send_response(503)
        self.end_headers()
        self.wfile.write(b"fixture delivery unavailable")

    def log_message(self, *_):
        pass


def local_failure_checks(work, instance):
    with http.server.ThreadingHTTPServer(("127.0.0.1", 0), RejectReceiver) as server:
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            fixture = Fixture(work, instance + "-local", "")
            with fixture.daemon() as (path, ready):
                require("alertmanager: disabled" in path.read_text(), "unset must disable")
                require("not configured" in fixture.emit(success=False), "CLI must reject unset URL")
                fixture.honeybee()
                require(ready() and RejectReceiver.posts == 0, "disabled emitter made a POST")
            fixture.configure(f"http://127.0.0.1:{server.server_port}")
            fixture.git("add", "-A")
            fixture.git("commit", "-qm", "local failing receiver")
            with fixture.daemon() as (path, ready):
                wait_for("failed delivery retry", lambda: RejectReceiver.posts >= 2, timeout=35)
                fixture.honeybee()  # no workable submodule is neutral, preflight still succeeds
                require(ready(), "failed delivery stopped frontend")
                require("retry in" in path.read_text() and "503" in path.read_text(),
                        "delivery failure was not logged/backed off")
                require("503" in fixture.emit(success=False), "one-shot must surface failure")
            log(f"disabled: 0 POSTs; failed receiver: {RejectReceiver.posts} POST attempts; "
                "frontend GET and honeybee preflight succeeded in both modes")
        finally:
            server.shutdown()
            thread.join(timeout=5)


def expected_alerts(fixture, health):
    instance = fixture.name
    base = {"service": "beehive", "instance": instance}
    since = health["preflight"]["first_fail_at"]
    since = instant(since).strftime("%Y-%m-%dT%H:%M:%SZ")
    error = health["preflight"]["last_error"]
    p = health["passes"]["fixture"]
    return {
        "BeehiveNeedsHuman": {
            "labels": dict(base, alertname="BeehiveNeedsHuman", severity="critical",
                           submodule="fixture", task="live-t1", category="secret"),
            "annotations": {"summary": "beehive fixture:live-t1 needs a human (secret)",
                            "escalation_summary": REASON, "description": REASON},
            "generatorURL": GENERATOR + "/submodule/fixture/plan#live-t1"},
        "BeehiveSwarmWedged": {
            "labels": dict(base, alertname="BeehiveSwarmWedged", severity="critical"),
            "annotations": {"summary": f"beehive {instance} swarm wedged: 3 consecutive preflight failures",
                            "stage": "preflight", "consecutive": "3", "since": since,
                            "last_error": error,
                            "description": f"3 consecutive preflight failures since {since} (threshold 3); last: {error}"},
            "generatorURL": GENERATOR + "/"},
        "BeehivePassFailing": {
            "labels": dict(base, alertname="BeehivePassFailing", severity="warning", submodule="fixture"),
            "annotations": {"summary": f"beehive {instance}:fixture: 3 consecutive passes without progress",
                            "consecutive": "3", "since": p["first_fail_at"],
                            "last_error": p["last_error"], "last_session": p["last_session"],
                            "description": f'3 consecutive failed/timed-out/no-progress passes since {p["first_fail_at"]} (threshold 3); last: {p["last_error"]}'},
            "generatorURL": GENERATOR + "/submodule/fixture/plan"},
    }


def verify_schema(alerts, expected):
    require(len(alerts) == 3, f"expected exactly three deduped alerts: {alerts}")
    require({a["labels"]["alertname"] for a in alerts} == set(expected), f"wrong families: {alerts}")
    for a in alerts:
        want = expected[a["labels"]["alertname"]]
        for field in ("labels", "annotations", "generatorURL"):
            require(a.get(field) == want[field], f"exact {field} mismatch: {a} != {want}")


def live_checks(work, instance, endpoint):
    fixture = Fixture(work, instance, endpoint)
    url = endpoint + "/api/v2/alerts?" + urllib.parse.urlencode(
        {"active": "true", "silenced": "true", "inhibited": "true", "filter": f'instance="{instance}"'})

    def alerts():
        result = json.loads(get(url))
        require(all(a["labels"]["instance"] == instance for a in result), "instance filter not honored")
        return result

    try:
        fixture.git("remote", "add", "origin", str(work / "does-not-exist.git"))
        for _ in range(3):
            require("preflight" in fixture.honeybee(success=False), "expected actual preflight failure")
        fixture.git("remote", "remove", "origin")
        health = json.loads(fixture.health.read_text())
        require(health["preflight"]["consecutive"] == 3, "preflight outcomes not persisted")
        # Repository input, not an API payload. The production HealthSource must
        # interpret this independently and emit the exact schema asserted below.
        health["passes"] = {"fixture": {"consecutive": 3, "first_fail_at": timestamp(),
                                        "last_fail_at": timestamp(), "last_session": "fixture-failed-pass-3",
                                        "last_error": "fixture turn cap without progress"}}
        fixture.health.write_text(json.dumps(health))
        expected = expected_alerts(fixture, health)
        with fixture.daemon():
            first = wait_for("three production alerts", lambda: alerts() or False)
            verify_schema(first, expected)
            log("GET initial exact schemas: " + json.dumps(first, sort_keys=True))
            baseline = {a["labels"]["alertname"]: a for a in first}
            for a in first:
                ttl = (instant(a["endsAt"]) - instant(a["startsAt"])).total_seconds()
                require(239 <= ttl <= 241, f"expected explicit four-minute TTL, got {ttl}")

            def refreshed():
                current = alerts()
                verify_schema(current, expected)
                for a in current:
                    prev = baseline[a["labels"]["alertname"]]
                    require(a["startsAt"] == prev["startsAt"], "refresh changed startsAt")
                return current if all((instant(a["endsAt"]) - instant(baseline[a["labels"]["alertname"]]["endsAt"])).total_seconds() >= 55
                                      for a in current) else False

            second = wait_for("background refresh extending expiry (no one-shot)", refreshed, timeout=100)
            log("GET background refresh/dedup: " + json.dumps(
                [{k: a[k] for k in ("labels", "startsAt", "endsAt")} for a in second], sort_keys=True))
            # Recover through real preflight, then clear the committed task and
            # per-pass fixture. Background polling, not a cleanup POST, resolves.
            fixture.honeybee()
            require(json.loads(fixture.health.read_text())["preflight"]["consecutive"] == 0,
                    "successful preflight did not reset the wedge")
            fixture.clear()
            wait_for("explicit background resolution", lambda: not alerts(), timeout=35)
            require(all(dt.datetime.now(dt.timezone.utc) < instant(a["endsAt"]) for a in first),
                    "resolution only observed after natural expiry")
            log("GET resolution: [] before original endsAt; all three resolved by background emitter")
        # Cross-process persistence also reconciles an empty desired set.
        log(fixture.emit().strip())
        require(not alerts(), "restart re-fired a cleared condition")
    finally:
        # Production cleanup affects only this random instance and fixture. A
        # failed cleanup fails the check, instead of leaking test alerts silently.
        fixture.clear()
        log("cleanup " + fixture.emit().strip())


def main():
    work = Path(sys.argv[1]).resolve()
    endpoint = os.environ["ALERTMANAGER_URL"].rstrip("/")
    instance = f"beehive-live-test-{int(time.time())}-{os.getpid()}"
    log(f"instance={instance}; endpoint={endpoint}; direct HTTPS/HTTP (no proxy payloads)")
    local_failure_checks(work, instance)
    live_checks(work, instance, endpoint)
    log(f"PASS ({instance})")


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        log(f"FAIL: {error}")
        raise
