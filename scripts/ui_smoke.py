#!/usr/bin/env python3
"""Exercise the embedded UI against a real supervisor (standard library only)."""
import json
import re
import signal
import subprocess
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

with tempfile.TemporaryDirectory() as directory:
    path = Path(directory) / "heron.yaml"
    path.write_text(f"""port: 0
apps:
  db:
    pwd: {directory}
    launch: sleep 120
  api:
    pwd: {directory}
    launch: sleep 120
    dependsOn: [db]
  web:
    pwd: {directory}
    launch: sleep 120
    dependsOn: [api]
""")
    # Pick a free proxy port; the UI is served from this same listener.
    import socket
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    path.write_text(path.read_text().replace("port: 0", f"port: {port}"))
    proc = subprocess.Popen(["./heron-test", "ui", "-c", str(path)],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    try:
        import selectors
        selector = selectors.DefaultSelector()
        selector.register(proc.stdout, selectors.EVENT_READ)
        assert selector.select(15), "UI did not publish its address"
        line = proc.stdout.readline().strip()
        base = f"http://ui.heron.localhost:{port}"
        assert line == f"Heron UI: {base}", line
        # Some CI images do not resolve reserved .localhost names. Connect to
        # loopback directly while retaining the public Host header.
        direct = f"http://127.0.0.1:{port}"
        host = f"ui.heron.localhost:{port}"
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        html = opener.open(urllib.request.Request(direct, headers={"Host": host}), timeout=5).read().decode()
        token = re.search(r'name="heron-token"\s+content="([^"]+)"', html).group(1)

        def api(endpoint, body=None):
            req = urllib.request.Request(direct + "/api/" + endpoint,
                data=None if body is None else json.dumps(body).encode(),
                headers={"Host": host, "X-Heron-Token": token, "Content-Type": "application/json"})
            return json.load(opener.open(req, timeout=20))

        api("action", {"ID": "web", "Action": "start"})
        for action in ("stop", "restart"):
            try:
                api("action", {"ID": "db", "Action": action})
                raise AssertionError("dependency protection bypassed")
            except urllib.error.HTTPError as err:
                assert err.code == 409 and b"api" in err.read()
        for app in ("web", "api", "db"):
            api("action", {"ID": app, "Action": "stop"})
        cfg = api("config?app=web")
        original = path.read_text()
        try:
            api("config?app=web", {"YAML": f"pwd: {directory}\nlaunch: sleep 120\ndependsOn: [missing]\n", "Version": cfg["version"]})
            raise AssertionError("invalid configuration accepted")
        except urllib.error.HTTPError as err:
            assert err.code == 400
        assert path.read_text() == original
        print("UI smoke passed: assets, transitive startup, stop/restart blockers, shutdown, config validation")
    finally:
        proc.send_signal(signal.SIGINT)
        try:
            proc.communicate(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.communicate()
            raise
        assert proc.returncode == 0, f"Heron exited {proc.returncode}"
