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
ENV = dict(os.environ, FLIP_HOME=str(ROOT / "state"), FLIP_CONFIG="")
HIDDEN = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
checks = []
listeners = []
ports = set()


def run(*args, cwd=ROOT, check=True):
    result = subprocess.run(args, cwd=cwd, env=ENV, capture_output=True, text=True, **HIDDEN)
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
const name=process.env.NAME||require('node:path').basename(process.cwd());
let healthy=true;
console.log('started '+name+' '+process.env.SERVICE);
http.createServer((q,r)=>{
  console.log(q.url);
  if(q.url==='/unhealthy') healthy=false;
  if(q.url==='/health') {r.statusCode=healthy?204:503;r.end();return;}
  r.end(JSON.stringify({name,service:process.env.SERVICE,path:q.url,pid:process.pid}));
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
template = {label: {**service, "dir": ".", "port": 0,
                   "env": {k: v for k, v in service.get("env", {}).items() if k != "NAME"}}
            for label, service in c["worktrees"]["feature-a"]["services"].items()}
configured = {**c, "worktrees": {"main": c["worktrees"]["main"]},
              "discover": {"repo": "main", "preview": True, "port_min": 41000, "port_max": 41999,
                           "services": template, "routes": c["worktrees"]["main"]["routes"]}}
(ROOT / "flip.json").write_text(json.dumps(configured))
for sock in listeners:
    sock.close()
assert "flip discover" in flip("doctor", check=False).stderr
assert not (ROOT / "state").exists()
assert not (ROOT / ".flip").exists()
discovered = flip("discover").stdout
for row in discovered.splitlines()[1:]:
    name, service, port, preview, _ = row.split("\t")
    c["worktrees"][name]["services"][service]["port"] = int(port)
    c["worktrees"][name]["preview_port"] = int(preview)
    ports.update(p for p in [int(port), int(preview)] if p)
flip("register", "fixture")
state_file = ROOT / "state/state.json"
saved = (state_file.read_bytes(), state_file.stat().st_mtime_ns)
assert flip("doctor").returncode == 0
assert run(FLIP, "-project", "fixture", "doctor", cwd=ROOT / "feature-d").returncode == 0
assert (state_file.read_bytes(), state_file.stat().st_mtime_ns) == saved
assert not (ROOT / ".flip").exists()
passed("Discovery allocates five worktrees; doctor resolves registration without allocating or writing state")

follower = None
try:
    for name in names:
        flip("up", name)
    assert flip("supervisor", "status").returncode == 0
    status = flip("status").stdout
    assert status.count("running:") == 11 and status.count("disabled") == 4, status
    passed("Automatic supervisor starts ten HTTP services and one opted-in worker across five worktrees")
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
    follower = subprocess.Popen([FLIP, "-config", str(ROOT / "flip.json"), "logs", "main", "web", "-n", "1", "-f"], env=ENV, stdout=subprocess.PIPE, text=True, **HIDDEN)
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
    for _ in range(20):
        assert request(base + "/picker/api", {"Action": "use", "Name": "main"}, {**headers, "Origin": "http://elsewhere.example"})[0] == 403
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
    flip("supervisor", "stop")
    assert "started main web" in flip("logs", "main", "web").stdout
    (ROOT / "feature-a/app.js").write_text(app)
    configured["idle_timeout_seconds"] = 2
    (ROOT / "flip.json").write_text(json.dumps(configured))
    flip("main")
    assert request(base + "/picker/api", {"Action": "status"}, headers)[0] == 403
    fresh_grant = flip("picker").stdout.strip().split("#")[1]
    headers["Authorization"] = "Bearer " + json.loads(request(base + "/picker/session", {"Grant": fresh_grant}, headers)[1])["session"]
    deadline = time.monotonic() + 3
    while time.monotonic() < deadline:
        flip("doctor")
        assert request(base + "/picker/api", {"Action": "status"}, headers)[0] == 200
        time.sleep(.25)
    assert "running:" not in flip("status").stdout
    flip("main")
    assert flip("status").stdout.count("running:") == 3
    passed("Doctor and picker status allow idle expiry; CLI restarts services and supervisor restart revokes browser sessions")
finally:
    if follower is not None:
        follower.terminate(); follower.wait(timeout=5)
    flip("supervisor", "stop", check=False)
    (ROOT / "report.json").write_text(json.dumps({"checks": checks}, indent=2))
for port in ports:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", port))
passed("All owned processes stopped and reserved ports released; port 8080 untouched")
(ROOT / "report.json").write_text(json.dumps({"checks": checks}, indent=2))
print("Report:", ROOT / "report.json")
