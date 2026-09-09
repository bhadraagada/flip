"""Five disposable Git worktrees. Requires Git, Node and a worktree-local Flip build.
Run: py test_experience.py --flip ./flip.exe
"""
import argparse
import json
import os
from pathlib import Path
import queue
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--flip", required=True)
args = parser.parse_args()
FLIP = str(Path(args.flip).resolve())
NODE = shutil.which("node")
assert NODE, "Node must be installed"
scratch = Path(__file__).resolve().parent / "work"
scratch.mkdir(exist_ok=True)
ROOT = Path(tempfile.mkdtemp(prefix="experience-", dir=scratch))
HIDDEN = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
checks = []
listeners = []
ports = set()


def run(*args, cwd=ROOT, check=True):
    result = subprocess.run(args, cwd=cwd, capture_output=True, text=True, **HIDDEN)
    if check:
        assert result.returncode == 0, result.stderr
    return result


def flip(*args, check=True):
    return run(FLIP, "-config", str(ROOT / "flip.json"), *args, check=check)


def passed(text):
    checks.append(text)
    print("PASS", text, flush=True)


def reserve():
    sock = socket.socket()
    sock.bind(("127.0.0.1", 0))
    listeners.append(sock)
    port = sock.getsockname()[1]
    assert port != 8080 and port not in ports
    ports.add(port)
    return port


def request(url, body=None, headers=None):
    req = urllib.request.Request(url, data=json.dumps(body).encode() if body is not None else None,
                                 headers=headers or {})
    try:
        response = urllib.request.urlopen(req, timeout=5)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        return response.status, response.read().decode()


def wait(check):
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            if check():
                return
        except OSError:
            pass
        time.sleep(.05)
    raise AssertionError("Timed out")


repo = ROOT / "main"
repo.mkdir()
run("git", "init", "-b", "main", cwd=repo)
app = '''const http=require('node:http');
let healthy=true;
console.log('started '+process.env.NAME+' '+process.env.SERVICE);
http.createServer((q,r)=>{
  console.log(q.url);
  if(q.url==='/unhealthy') healthy=false;
  if(q.url==='/health') {r.statusCode=healthy?204:503;r.end();return;}
  r.end(JSON.stringify({name:process.env.NAME,service:process.env.SERVICE,path:q.url,pid:process.pid}));
}).listen(+process.env.PORT,'127.0.0.1');
'''
(repo / "app.js").write_text(app)
(repo / "worker.js").write_text("console.log('worker started');setInterval(()=>{},1000);")
run("git", "add", ".", cwd=repo)
run("git", "-c", "user.name=Flip Test", "-c", "user.email=flip-test@localhost", "commit", "-m", "Fixture", cwd=repo)
names = ["main", "feature-a", "feature-b", "feature-c", "feature-d"]
c = {"port": reserve(), "control_port": reserve(), "timeout_seconds": 2, "worktrees": {}}
for name in names:
    directory = ROOT / name
    if name != "main":
        run("git", "worktree", "add", "-b", name, str(directory), cwd=repo)
    services = {}
    for service in ["web", "api"]:
        services[service] = {"dir": str(directory), "command": [NODE, "app.js"], "port": reserve(),
                             "health": "/health", "env": {"NAME": name, "SERVICE": service, "PORT": "{port}"}}
    services["worker"] = {"type": "worker", "enabled": name == "main", "dir": str(directory),
                          "command": [NODE, "worker.js"]}
    c["worktrees"][name] = {"preview_port": reserve(), "services": services,
                             "routes": [{"prefix": "/", "service": "web"},
                                        {"prefix": "/api", "service": "api", "strip_prefix": True}]}
(ROOT / "flip.json").write_text(json.dumps(c))
for sock in listeners:
    sock.close()
assert flip("doctor").returncode == 0
assert not (ROOT / ".flip").exists()
passed("Offline doctor validates five worktrees without creating state")

log = (ROOT / "supervisor.log").open("w")
supervisor = subprocess.Popen([FLIP, "-config", str(ROOT / "flip.json"), "serve"], stdout=log, stderr=log, **HIDDEN)
follower = None
try:
    wait(lambda: (ROOT / ".flip/token").exists())
    for name in names:
        flip("up", name)
    status = flip("status").stdout
    assert status.count("running:") == 11 and status.count("disabled") == 4, status
    passed("Ten HTTP services and one opted-in worker run across five real worktrees")
    public = f"http://127.0.0.1:{c['port']}"
    for name in names:
        flip(name)
        result = json.loads(request(public + "/api/value")[1])
        assert result["name"] == name and result["service"] == "api" and result["path"] == "/value", result
        for path in ["/", "/oauth/callback", "/apiculture"]:
            result = json.loads(request(public + path)[1])
            assert result["name"] == name and result["service"] == "web", result
    for name in names:
        own = f"http://127.0.0.1:{c['worktrees'][name]['preview_port']}"
        assert json.loads(request(own)[1])["name"] == name
    passed("Shared switching and independent preview routes stay matched, including callback and API boundaries")
    before = flip("status").stdout
    assert flip("doctor").returncode == 0
    assert flip("status").stdout == before
    passed("Live doctor preserves all PIDs and selection")
    assert "worker started" in flip("logs", "main", "worker").stdout
    follower = subprocess.Popen([FLIP, "-config", str(ROOT / "flip.json"), "logs", "main", "web", "-n", "1", "-f"], stdout=subprocess.PIPE, text=True, **HIDDEN)
    lines = queue.Queue()
    threading.Thread(target=lambda: [lines.put(line) for line in follower.stdout], daemon=True).start()
    lines.get(timeout=5)
    flip("restart", "main", "web")
    deadline = time.monotonic() + 5
    while "started main web" not in lines.get(timeout=5):
        assert time.monotonic() < deadline
    follower.terminate(); follower.wait(timeout=5); follower = None
    passed("CLI logs reads workers and follows new output across HTTP service restart")

    base = f"http://127.0.0.1:{c['control_port']}"
    login_link = flip("picker").stdout.strip()
    grant = login_link.split("#")[1]
    token = (ROOT / ".flip/token").read_text()
    headers = {"Origin": base, "Content-Type": "application/json"}
    code, body = request(base + "/picker/session", {"Grant": grant}, headers)
    assert code == 200
    session = json.loads(body)["session"]
    assert session != token and session != grant
    assert request(base + "/picker/session", {"Grant": grant}, headers)[0] == 403
    headers["Authorization"] = "Bearer " + session
    for path in ["/picker", "/picker.js", "/picker.css"]:
        body = request(base + path)[1]
        assert token not in body and session not in body and grant not in body
    assert request(base + "/picker/api", {"Action": "use", "Name": "main"}, {**headers, "Origin": "http://evil.example"})[0] == 403
    assert request(base, {"Action": "down", "Name": "main"}, {"Authorization": "Bearer " + session, "Content-Type": "application/json"})[0] == 403
    code, body = request(base + "/picker/api", {"Action": "use", "Name": "main"}, headers)
    state = json.loads(body)
    assert code == 200 and len(state["Worktrees"]) == 5
    assert next(w for w in state["Worktrees"] if w["Selected"])["Name"] == "main"
    assert all(w["URL"] for w in state["Worktrees"])
    passed("Picker grants are single-use, scoped and absent from assets; authenticated switching works")

    flip("down", "feature-a")
    (ROOT / "feature-a/app.js").write_text("throw new Error('fixture startup failure');")
    assert request(base + "/picker/api", {"Action": "use", "Name": "feature-a"}, headers)[0] == 400
    assert json.loads(request(public)[1])["name"] == "main"
    assert "fixture startup failure" in flip("logs", "feature-a", "api").stdout
    passed("Failed picker switch retains selection and exposes service logs")
    web_port = c["worktrees"]["main"]["services"]["web"]["port"]
    request(f"http://127.0.0.1:{web_port}/unhealthy")
    before = flip("status").stdout
    failed = flip("doctor", check=False)
    assert failed.returncode != 0 and "HTTP 503" in failed.stdout and "flip logs main web" in failed.stdout
    assert flip("status").stdout == before
    with socket.socket() as unrelated:
        unrelated.bind(("127.0.0.1", c["worktrees"]["feature-a"]["services"]["web"]["port"]))
        failed = flip("doctor", check=False)
        assert "FAIL  feature-a/web port" in failed.stdout
        assert unrelated.getsockname()[1] > 0
    passed("Doctor reports readiness failure and unrelated port collision without changing processes")
finally:
    if follower is not None:
        follower.terminate(); follower.wait(timeout=5)
    for name in names:
        flip("down", name, check=False)
    supervisor.terminate(); supervisor.wait(timeout=10); log.close()
    (ROOT / "report.json").write_text(json.dumps({"checks": checks}, indent=2))
for port in ports:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", port))
passed("All owned processes stopped and reserved ports released; port 8080 untouched")
(ROOT / "report.json").write_text(json.dumps({"checks": checks}, indent=2))
print("Report:", ROOT / "report.json")
