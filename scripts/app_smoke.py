"""Verify eager --app startup, dependency order, isolation and TUI failure inspection."""
import fcntl
import json
import os
import pty
import select
import signal
import socket
import struct
import subprocess
import tempfile
import termios
import time


def port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


for interactive in (False, True):
    for fail in (False, True):
        with tempfile.TemporaryDirectory() as tmp:
            events = os.path.join(tmp, 'events')
            def command(name):
                return json.dumps(f'echo {name} >> {events}')
            # Unrelated occupied backend must not prevent selected startup.
            with socket.socket() as occupied:
                occupied.bind(('127.0.0.1', 0))
                occupied.listen()
                config = os.path.join(tmp, 'heron.yaml')
                with open(config, 'w') as out:
                    out.write(f'''port: {port()}
stopTimeout: 2s
startTimeout: 2s
scheduledTasks:
  unrelatedJob:
    command: {command('job')}
    every: 1s
    runOnStart: true
apps:
  db:
    pwd: {tmp}
    build: {command('db')}
    launch: sleep 60
    idle: 0
  cache:
    pwd: {tmp}
    dependsOn: [db]
    build: {command('cache')}
    launch: sleep 60
    idle: 0
  api:
    pwd: {tmp}
    dependsOn: [cache, db]
    build: {'exit 7' if fail else command('api')}
    launch: sleep 60
    idle: 0
  unrelated:
    pwd: {tmp}
    build: {command('unrelated')}
    launch: sleep 60
    path: /unrelated
    port: {occupied.getsockname()[1]}
''')
                master, slave = pty.openpty()
                fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 30, 120, 0, 0))
                args = ['./heron-test'] + (['tui'] if interactive else [])
                args += ['-c', config] + (['--app', 'api'] if interactive else ['--app=api'])
                child = subprocess.Popen(args, stdin=slave, stdout=slave, stderr=slave)
                transcript = bytearray()
                def wait_for(predicate):
                    deadline = time.monotonic() + 15
                    while not predicate():
                        assert time.monotonic() < deadline, transcript.decode(errors='replace')
                        if select.select([master], [], [], .05)[0]:
                            transcript.extend(os.read(master, 65536))
                        if child.poll() is not None:
                            assert predicate(), transcript.decode(errors='replace')
                try:
                    if fail:
                        if interactive:
                            wait_for(lambda: b'[failed]' in transcript)
                            os.write(master, b'e')
                            wait_for(lambda: b'app startup failed' in transcript)
                            assert child.poll() is None
                        else:
                            wait_for(lambda: child.poll() is not None)
                            assert child.returncode != 0
                    else:
                        wait_for(lambda: os.path.exists(events) and open(events).read() == 'db\ncache\napi\n')
                    if child.poll() is None:
                        if interactive:
                            os.write(master, b'q')
                            wait_for(lambda: b'Confirm quit' in transcript)
                            os.write(master, b'y')
                        else:
                            child.send_signal(signal.SIGTERM)
                        wait_for(lambda: child.poll() is not None)
                        assert child.returncode == 0, transcript.decode(errors='replace')
                    with open(events) as inp:
                        assert inp.read() == ('db\ncache\n' if fail else 'db\ncache\napi\n')
                finally:
                    if child.poll() is None:
                        child.kill()
                    child.wait()
                    os.close(master)
                    os.close(slave)
print('--app CLI/TUI smoke passed (startup, dependencies, isolation, failure, shutdown)')
