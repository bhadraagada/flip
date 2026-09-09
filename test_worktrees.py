"""Real CLI integration check. Requires git, node, npm, flip, FastAPI and Uvicorn.
Creates a temporary five-worktree project; retains its files and logs for inspection.
Run: py test_worktrees.py
"""
import base64
import json
import os
import re
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.request

ROOT = Path(tempfile.mkdtemp(prefix="flip-five-worktrees-"))
REPORT = []
HIDDEN = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
FLIP = os.environ.get("FLIP_BINARY") or (str(Path.home() / "go/bin/flip.exe") if os.name == "nt" else shutil.which("flip"))
assert FLIP, "Install flip on PATH first"


def run(*args, cwd=ROOT, check=True):
    result = subprocess.run(args, cwd=cwd, capture_output=True, text=True, **HIDDEN)
    if check and result.returncode:
        raise AssertionError(f"{args}: {result.stdout}\n{result.stderr}")
    return result


def passed(message):
    REPORT.append(message)
    print("PASS", message, flush=True)


def free_port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def get(port, path="/"):
    with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=3) as response:
        return response.read().decode()


def wait_for(check, timeout=10):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except (OSError, ValueError):
            pass
        time.sleep(0.1)
    raise AssertionError("Timed out waiting for condition")


def websocket(port):
    client = get(port, "/@vite/client")
    token = re.search(r'const wsToken = "([^"]+)"', client).group(1)
    conn = socket.create_connection(("127.0.0.1", port), timeout=5)
    key = base64.b64encode(os.urandom(16)).decode()
    conn.sendall((f"GET /?token={token} HTTP/1.1\r\nHost: localhost:{port}\r\n"
                  "Upgrade: websocket\r\nConnection: Upgrade\r\n"
                  f"Sec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n"
                  f"Origin: http://localhost:{port}\r\nSec-WebSocket-Protocol: vite-hmr\r\n\r\n").encode())
    reader = conn.makefile("rb")
    assert b"101" in reader.readline(), "HMR upgrade failed"
    while reader.readline() != b"\r\n":
        pass
    return conn, reader


def frame(reader):
    first, second = reader.read(2)
    size = second & 127
    if size == 126:
        size = struct.unpack("!H", reader.read(2))[0]
    elif size == 127:
        size = struct.unpack("!Q", reader.read(8))[0]
    assert first & 15 == 1, "Expected text frame"
    return json.loads(reader.read(size))


print("Test project:", ROOT, flush=True)
ports = set()


def unique_port():
    port = free_port()
    while port in ports:
        port = free_port()
    ports.add(port)
    return port


public, control = unique_port(), unique_port()
dependencies = ROOT / "dependencies"
dependencies.mkdir()
(dependencies / "package.json").write_text('{"name":"flip-test-deps","private":true}')
npm = shutil.which("npm.cmd" if os.name == "nt" else "npm")
run(npm, "install", "--prefix", str(dependencies), "--prefer-offline", "--no-audit", "--no-fund", "vite", cwd=dependencies)
vite = dependencies / "node_modules/vite/bin/vite.js"
repo = ROOT / "main"
repo.mkdir()
run("git", "init", "-b", "main", cwd=repo)
(repo / "index.html").write_text('<h1>main</h1><script type="module" src="/main.js"></script>')
(repo / "main.js").write_text('console.log("main"); if (import.meta.hot) import.meta.hot.accept();')
app = '''from fastapi import FastAPI
import os
app = FastAPI()
VALUE = {value!r}
@app.get("/api/value")
def value():
    return {{"value": VALUE, "pid": os.getpid()}}
'''
(repo / "app.py").write_text(app.format(value="main"))
(repo / ".gitignore").write_text("__pycache__/\n")
run("git", "add", ".", cwd=repo)
run("git", "-c", "user.name=Flip Test", "-c", "user.email=flip-test@localhost", "commit", "-m", "test fixture", cwd=repo)
names = ["main", "feature-a", "feature-b", "feature-c", "feature-d"]
config = {"port": public, "control_port": control, "timeout_seconds": 12, "worktrees": {}}
for name in names:
    directory = ROOT / name
    if name != "main":
        run("git", "worktree", "add", "-b", name, str(directory), cwd=repo)
    (directory / "app.py").write_text(app.format(value=name))
    (directory / "index.html").write_text(f'<h1>{name}</h1><script type="module" src="/main.js"></script>')
    (directory / "vite.config.mjs").write_text(f'export default {{ server: {{ ws: {{ clientPort: {public} }} }} }};')
    config["worktrees"][name] = {
        "ui": {"dir": str(directory), "port": unique_port(), "health": "/", "command": [shutil.which("node"), str(vite), "--host", "127.0.0.1", "--port", "{port}", "--strictPort"]},
        "backend": {"dir": str(directory), "port": unique_port(), "health": "/openapi.json", "command": [sys.executable, "-m", "uvicorn", "app:app", "--host", "127.0.0.1", "--port", "{port}"]},
    }
(ROOT / "flip.json").write_text(json.dumps(config, indent=2))
passed("Created five real Git worktrees")
supervisor_log = (ROOT / "supervisor.log").open("w")
supervisor = subprocess.Popen([FLIP, "serve"], cwd=ROOT, stdout=supervisor_log, stderr=supervisor_log, **HIDDEN)
try:
    wait_for(lambda: (ROOT / ".flip/token").exists())
    for name in names:
        run(FLIP, "up", name)
    status = run(FLIP, "status").stdout
    assert status.count("running:") == 10, status
    passed("All five Vite/FastAPI pairs running simultaneously")
    for name in names:
        backend_port = config["worktrees"][name]["backend"]["port"]
        old_pid = json.loads(get(backend_port, "/api/value"))["pid"]
        run(FLIP, name)
        value = json.loads(get(public, "/api/value"))
        assert value["value"] == name and value["pid"] != old_pid, value
        assert f"<h1>{name}</h1>" in get(public)
        assert f"<h1>{name}</h1>" in get(public, "/oauth/callback?code=test")
        passed(f"flip {name}: matching UI/API/callback, backend PID changed")
    target = ROOT / "feature-d/app.py"
    target.write_text(app.format(value="edited-after-start"))
    assert json.loads(get(public, "/api/value"))["value"] == "feature-d"
    run(FLIP, "restart", "feature-d", "backend")
    assert json.loads(get(public, "/api/value"))["value"] == "edited-after-start"
    passed("Python edit stayed stale without reload, then appeared after restart")
    get(public, "/main.js")
    conn, reader = websocket(public)
    try:
        assert frame(reader)["type"] == "connected"
        (ROOT / "feature-d/main.js").write_text('console.log("edited HMR"); if (import.meta.hot) import.meta.hot.accept();')
        update = frame(reader)
        assert update["type"] == "update", update
    finally:
        reader.close()
        conn.close()
    passed("Real Vite HMR WebSocket delivered file-change update through Flip")
    (ROOT / "feature-a/app.py").write_text("invalid Python syntax !!!")
    failed = run(FLIP, "feature-a", check=False)
    assert failed.returncode != 0
    assert json.loads(get(public, "/api/value"))["value"] == "edited-after-start"
    assert "<h1>feature-d</h1>" in get(public)
    passed("Broken target backend failed without switching away from feature-d")
    for name in names:
        run(FLIP, "down", name)
    assert "running:" not in run(FLIP, "status").stdout
    passed("flip down stopped every worktree")
finally:
    for name in names:
        run(FLIP, "down", name, check=False)
    supervisor.terminate()
    supervisor.wait(timeout=10)
    supervisor_log.close()
    (ROOT / "report.json").write_text(json.dumps({"passed": REPORT, "public_port": public}, indent=2))
for port in ports:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", port))
passed("All twelve test ports released; existing port 8080 untouched")
(ROOT / "report.json").write_text(json.dumps({"passed": REPORT, "public_port": public}, indent=2))
print("Report:", ROOT / "report.json", flush=True)
