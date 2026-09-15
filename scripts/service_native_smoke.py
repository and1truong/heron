#!/usr/bin/env python3
"""Opt-in by capability: exercise actual launchd/systemd user service commands."""
import concurrent.futures
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import socket
import subprocess
import tempfile
import urllib.request


def manager_available():
    if platform.system() == "Darwin":
        cmd = ["launchctl", "print", f"gui/{os.getuid()}"]
    elif platform.system() == "Linux":
        cmd = ["systemctl", "--user", "show", "--property=Version"]
    else:
        return False
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=5)
        return result.returncode == 0 and (platform.system() == "Darwin" or "Version=" in result.stdout)
    except (OSError, subprocess.TimeoutExpired):
        return False


if not manager_available():
    print("Native service smoke SKIPPED: no launchd login session/systemd user manager")
    raise SystemExit(0)

binary = str(Path("heron-test").resolve())
with tempfile.TemporaryDirectory(prefix="heron native ") as directory:
    cfg = Path(directory).resolve() / "config.yaml"
    identity = "heron-" + hashlib.sha256(str(cfg).encode()).hexdigest()[:32]
    state_dir = Path.home() / ".local/state/heron" / identity
    unit_link = Path.home() / ".config/systemd/user" / (identity + ".service")
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    cfg.write_text(json.dumps({"port": port, "apps": {}}))

    def command(action, expected=0):
        result = subprocess.run([binary, action, "-c", str(cfg), "--timeout", "30s"],
                                capture_output=True, text=True, timeout=40)
        assert result.returncode == expected, (action, result.returncode, result.stdout, result.stderr)
        return result.stdout

    try:
        command("status", 3)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(lambda _: command("start"), range(2)))
        first = command("status")
        first_pid = re.search(r"PID: (\d+)", first).group(1)
        host = f"ui.heron.localhost:{port}"
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        assert opener.open(urllib.request.Request(f"http://127.0.0.1:{port}/", headers={"Host": host}), timeout=5).status == 200
        cfg.write_text("apps: [invalid]")
        command("restart", 1)
        assert f"PID: {first_pid}\n" in command("status"), "invalid restart stopped healthy service"
        cfg.write_text(json.dumps({"port": port, "apps": {}}))
        command("restart")
        assert re.search(r"PID: (\d+)", command("status")).group(1) != first_pid
        cfg.unlink()
        command("status")
        command("stop")
        command("status", 3)
        command("stop")
        print("Native service smoke passed: concurrent start, automatic UI, status, invalid restart preservation, full restart, missing config stop, idempotent stop")
    finally:
        # Clean only this test's instance, including its never-enabled unit link.
        command("stop")
        if platform.system() == "Linux" and unit_link.is_symlink() and unit_link.resolve() == state_dir / (identity + ".service"):
            unit_link.unlink()
            subprocess.run(["systemctl", "--user", "daemon-reload"], check=True)
        if state_dir.exists():
            shutil.rmtree(state_dir)
