#!/usr/bin/env python3
"""Exercise the real service worker without requiring an OS service manager.

The worker is launched in a separate session with no terminal, as launchd/systemd
would launch it. Native command rendering/status parsing have separate Go tests.
"""
import json
import os
from pathlib import Path
import re
import signal
import socket
import subprocess
import tempfile
import time
import urllib.request


def wait_for(predicate, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = predicate()
        if result:
            return result
        time.sleep(0.05)
    raise AssertionError("timed out waiting for service")


binary = str(Path("heron-test").resolve())
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
with tempfile.TemporaryDirectory(prefix="heron service ") as directory:
    root = Path(directory)
    cfg = root / "config.yaml"
    specpath = root / "spec.json"
    statepath = root / "runtime.json"
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    config = {
        "port": port,
        "startUp": ['printf "%s" "$HERON_SERVICE_SMOKE" > startup-marker'],
        "tearDown": ["echo done > teardown-marker"],
        "apps": {
            "db": {"pwd": directory, "launch": "echo persistent-service-log; exec sleep 120"},
            "api": {"pwd": directory, "launch": "exec sleep 120", "dependsOn": ["db"]},
        },
    }
    cfg.write_text(json.dumps(config))
    spec = {
        "Config": str(cfg), "Directory": directory, "Executable": binary,
        "Environment": [f"{k}={v}" for k, v in os.environ.items()] + ["HERON_SERVICE_SMOKE=captured"],
        "Token": "smoke-generation",
    }
    specpath.write_text(json.dumps(spec))
    specpath.chmod(0o600)
    subprocess.run([binary, "__service-validate", str(specpath)], check=True)
    assert not (root / "startup-marker").exists(), "validation executed hooks"
    assert not statepath.exists(), "validation started the runtime"

    def state():
        try:
            return json.loads(statepath.read_text())
        except FileNotFoundError:
            return {}

    def launch():
        return subprocess.Popen([binary, "__service-run", str(specpath)],
                                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL, start_new_session=True)

    process = launch()
    try:
        wait_for(lambda: state().get("State") == "running")
        record = state()
        assert record["PID"] == process.pid and record["Token"] == spec["Token"], record
        assert record["URL"] == f"http://ui.heron.localhost:{port}", record
        assert (root / "startup-marker").read_text() == "captured"
        direct, host = f"http://127.0.0.1:{port}", f"ui.heron.localhost:{port}"
        html = opener.open(urllib.request.Request(direct, headers={"Host": host}), timeout=5).read().decode()
        token = re.search(r'name="heron-token"\s+content="([^"]+)"', html).group(1)

        def api(endpoint, body=None):
            request = urllib.request.Request(direct + "/api/" + endpoint,
                data=None if body is None else json.dumps(body).encode(),
                headers={"Host": host, "X-Heron-Token": token, "Content-Type": "application/json"})
            return json.load(opener.open(request, timeout=10))

        # Starting the service itself must leave applications lazy.
        assert "persistent-service-log" not in (root / "service.log").read_text()
        api("action", {"ID": "api", "Action": "start"})
        wait_for(lambda: "persistent-service-log" in (root / "service.log").read_text())
        # Stop must operate from the loaded runtime even if YAML disappears.
        cfg.unlink()
        process.send_signal(signal.SIGTERM)
        assert process.wait(timeout=15) == 0
        assert state()["State"] == "stopped", state()
        assert (root / "teardown-marker").read_text().strip() == "done"
        log = (root / "service.log").read_text()
        assert log.index("msg=stopping service=api") < log.index("msg=stopping service=db"), log
        assert (root / "service.log").stat().st_mode & 0o077 == 0
    finally:
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            process.wait(timeout=15)

    # Startup hook failures must never publish readiness.
    config["startUp"] = ["exit 7"]
    cfg.write_text(json.dumps(config))
    spec["Token"] = "failed-generation"
    specpath.write_text(json.dumps(spec))
    process = launch()
    assert process.wait(timeout=15) != 0
    assert state()["State"] == "failed" and state()["Token"] == "failed-generation", state()
    assert state()["Error"], state()
    print("Service smoke passed: detached worker, automatic UI, readiness, lazy apps, saved cwd/env, persistent logs, dependency shutdown, missing config, startup failure")
