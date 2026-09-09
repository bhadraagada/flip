"""Live named-service CLI check. Run: python test_services.py PATH_TO_LOCAL_FLIP_BINARY.
Uses only Python's standard library, Git, and a prebuilt Flip binary.
"""
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

FLIP = str(Path(sys.argv[1]).resolve())
ROOT = Path(tempfile.mkdtemp(prefix="flip-services-"))
HIDDEN = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
PORTS = []
RESERVATIONS = []


def port():
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    value = sock.getsockname()[1]
    assert value != 8080
    RESERVATIONS.append(sock)
    PORTS.append(value)
    return value


def run(*args, cwd=ROOT, check=True):
    result = subprocess.run(args, cwd=cwd, capture_output=True, text=True, timeout=30, **HIDDEN)
    if check:
        assert result.returncode == 0, result.stdout + result.stderr
    return result


def get(port, path="/"):
    try:
        with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=3) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode()


def wait_for(check):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except OSError:
            pass
        time.sleep(0.1)
    raise AssertionError("startup timed out")


app = '''import http.server, json, os, sys, time
from pathlib import Path
if sys.argv[1] == "worker":
    if Path("worker-fail").exists():
        sys.exit(2)
    with open("worker-pids.txt", "a") as f:
        f.write(str(os.getpid()) + "\\n")
    while True:
        time.sleep(1)
class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.end_headers()
        self.wfile.write(json.dumps({"tree": Path.cwd().name, "service": sys.argv[1], "path": self.path, "pid": os.getpid()}).encode())
http.server.ThreadingHTTPServer(("127.0.0.1", int(sys.argv[2])), Handler).serve_forever()
'''
repo = ROOT / "a"
repo.mkdir()
(repo / "app.py").write_text(app)
run("git", "init", "-b", "main", cwd=repo)
run("git", "add", "app.py", cwd=repo)
run("git", "-c", "user.name=Flip Test", "-c", "user.email=flip-test@localhost", "commit", "-m", "fixture", cwd=repo)
run("git", "worktree", "add", "-b", "b", str(ROOT / "b"), cwd=repo)
public, control, preview_a, preview_b = port(), port(), port(), port()


def http_service(name, tree):
    return {"dir": str(ROOT / tree), "port": port(), "command": [sys.executable, "app.py", name, "{port}"]}


config = {"port": public, "control_port": control, "timeout_seconds": 5, "worktrees": {
    "a": {"preview_port": preview_a, "services": {
        "site": http_service("site", "a"), "search": http_service("search", "a"),
        "jobs": {"type": "worker", "enabled": True, "restart_on_use": True, "dir": str(repo), "command": [sys.executable, "app.py", "worker"]},
        "disabled": {"type": "worker", "dir": str(repo), "command": ["deliberately-missing-worker"]},
    }, "routes": [{"prefix": "/", "service": "site"}, {"prefix": "/search", "service": "search", "strip_prefix": True}]},
    "b": {"preview_port": preview_b, "services": {"solo": http_service("solo", "b")}},
}}
(ROOT / "flip.json").write_text(json.dumps(config))
for sock in RESERVATIONS:
    sock.close()
log = (ROOT / "supervisor.log").open("w")
supervisor = subprocess.Popen([FLIP, "serve"], cwd=ROOT, stdout=log, stderr=log, **HIDDEN)
checks = []
try:
    wait_for(lambda: (ROOT / ".flip/token").exists())
    assert get(public)[0] == get(preview_a)[0] == get(preview_b)[0] == 503
    run(FLIP, "b")
    run(FLIP, "up", "a")
    assert json.loads(get(public)[1])["tree"] == "b"
    checks.append("up publishes independent preview without switching fixed selection")
    with ThreadPoolExecutor(max_workers=3) as pool:
        responses = list(pool.map(get, [public, preview_a, preview_b] * 5))
    assert [json.loads(body)["tree"] for status, body in responses] == ["b", "a", "b"] * 5
    checks.append("simultaneous distinct previews retain worktree routing")
    response = json.loads(get(preview_a, "/search/items?q=one")[1])
    assert response["service"] == "search" and response["path"] == "/items?q=one"
    assert json.loads(get(preview_a, "/searching")[1])["service"] == "site"
    assert json.loads(get(preview_a, "/oauth/callback?code=fixture")[1])["service"] == "site"
    checks.append("custom route, query, prefix stripping, boundary and callback paths")
    site_pid = json.loads(get(preview_a)[1])["pid"]
    worker_pid = (repo / "worker-pids.txt").read_text().splitlines()[-1]
    run(FLIP, "a")
    assert json.loads(get(preview_a)[1])["pid"] == site_pid
    assert (repo / "worker-pids.txt").read_text().splitlines()[-1] != worker_pid
    assert json.loads(get(preview_b)[1])["tree"] == "b"
    status = run(FLIP, "status").stdout
    assert "disabled\tdisabled" in status and status.count("running:") == 4
    checks.append("worker opt-in and restart_on_use, default HTTP PID stability")
    search_pid = json.loads(get(preview_a, "/search")[1])["pid"]
    run(FLIP, "restart", "a", "search")
    assert json.loads(get(preview_a, "/search")[1])["pid"] != search_pid
    assert run(FLIP, "restart", "a", "disabled", check=False).returncode != 0
    checks.append("restart accepts custom names and rejects disabled workers")
    run(FLIP, "b")
    run(FLIP, "down", "a")
    assert get(preview_a)[0] == 503
    (repo / "worker-fail").touch()
    assert run(FLIP, "a", check=False).returncode != 0
    assert get(preview_a)[0] == 503 and json.loads(get(public)[1])["tree"] == "b"
    checks.append("worker startup failure leaves existing selection and unpublished preview intact")
finally:
    for name in config["worktrees"]:
        run(FLIP, "down", name, check=False)
    supervisor.terminate()
    supervisor.wait(timeout=10)
    log.close()
for value in PORTS:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", value))
checks.append("all owned service and preview ports released; 8080 untouched")
(ROOT / "report.json").write_text(json.dumps({"passed": checks}, indent=2))
for check in checks:
    print("PASS", check)
print("Report:", ROOT / "report.json")
