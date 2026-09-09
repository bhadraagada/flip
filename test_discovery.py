"""Live discovery check. Run: python test_discovery.py /path/to/worktree-local/flip

Uses only Python's standard library, Git and the supplied Flip binary.
Creates five disposable Git worktrees and stops only the processes it starts.
"""

import concurrent.futures
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import urllib.request


def main():
    binary = Path(sys.argv[1]).resolve(strict=True)
    scratch = Path(__file__).resolve().parent / ".flip"
    scratch.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="discovery-", dir=scratch) as temporary:
        root = Path(temporary)
        repo = root / "repo"
        repo.mkdir()
        env = dict(os.environ, FLIP_HOME=str(root / "state"), FLIP_CONFIG="")

        def git(*args):
            subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True)

        def flip(*args, cwd=root, ok=True):
            result = subprocess.run([str(binary), *args], cwd=cwd, env=env, capture_output=True, text=True, timeout=20)
            if ok and result.returncode:
                raise AssertionError(result.stderr)
            return result

        def reserve():
            sock = socket.socket()
            sock.bind(("127.0.0.1", 0))
            sock.listen()
            return sock

        def get(port, path="/name.txt"):
            with urllib.request.urlopen(f"http://127.0.0.1:{port}{path}", timeout=3) as response:
                return response.read().decode().strip()

        git("init")
        (repo / "name.txt").write_text("repo")
        git("add", "name.txt")
        git("-c", "user.name=Flip Test", "-c", "user.email=flip@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
        names = ["repo"]
        for i in range(1, 5):
            name = f"tree-{i}"
            path = root / name
            git("worktree", "add", "--detach", str(path))
            (path / "name.txt").write_text(name)
            names.append(name)

        public, control, occupied = reserve(), reserve(), reserve()
        public_port, control_port, occupied_port = [s.getsockname()[1] for s in (public, control, occupied)]
        config = {
            "port": public_port, "control_port": control_port,
            "discover": {
                "repo": ".", "preview": True,
                "port_min": occupied_port, "port_max": 65535,
                "services": {"web": {"command": [sys.executable, "-m", "http.server", "{port}", "--bind", "127.0.0.1"]}},
            },
        }
        file = repo / "flip.json"
        file.write_text(json.dumps(config))
        supervisor = None
        try:
            with concurrent.futures.ThreadPoolExecutor(max_workers=6) as pool:
                assignments = list(pool.map(lambda _: flip("-config", str(file), "discover").stdout, range(6)))
            assert len(set(assignments)) == 1, "concurrent allocations differ"
            rows = [line.split("\t") for line in assignments[0].splitlines()[1:]]
            assert len(rows) == 5
            preview = {row[0]: int(row[3]) for row in rows}
            assigned_ports = [int(row[column]) for row in rows for column in (2, 3)]
            assert len(set(assigned_ports + [public_port, control_port, occupied_port])) == 13
            flip("-config", str(file), "register", "fixture")
            assert flip("-project", "fixture", "discover").stdout == assignments[0]
            assert flip("discover", cwd=root / "tree-4").stdout == assignments[0]
            nested = repo / "nested"
            nested.mkdir()
            assert flip("discover", cwd=nested).stdout == assignments[0]
            assert not (repo / ".flip" / "token").exists(), "discovery or registration started a supervisor"
            public.close()
            control.close()

            def start_supervisor():
                nonlocal supervisor
                # Mark ownership before invoking the CLI so failures still clean up.
                supervisor = True
                flip("-project", "fixture", "up", names[-1])
                assert "running:" in flip("-project", "fixture", "supervisor", "status").stdout

            def stop_supervisor():
                nonlocal supervisor
                if supervisor:
                    flip("-project", "fixture", "supervisor", "stop")
                    supervisor = None

            start_supervisor()
            for name in names:
                flip("-project", "fixture", "up", name)
                assert get(preview[name]) == name
            for name in names:
                flip("-project", "fixture", "use", name)
                assert get(public_port) == name
                assert all(get(preview[other]) == other for other in names)
            assert flip("-project", "fixture", "discover").stdout == assignments[0]
            stop_supervisor()
            start_supervisor()
            assert flip("-project", "fixture", "discover").stdout == assignments[0]
            for name in names:
                flip("-project", "fixture", "up", name)
                assert get(preview[name]) == name
            stop_supervisor()
            # A saved port occupied later fails startup without silently changing its assignment.
            stolen = socket.socket()
            stolen.bind(("127.0.0.1", int(rows[0][2])))
            stolen.listen()
            try:
                start_supervisor()
                failure = flip("-project", "fixture", "up", rows[0][0], ok=False)
                assert failure.returncode and "occupied" in failure.stderr, failure
                assert flip("-project", "fixture", "discover").stdout == assignments[0]
            finally:
                stop_supervisor()
                stolen.close()
            file.unlink()
            assert "unavailable" in flip("projects").stdout
            assert flip("-project", "fixture", "discover", ok=False).returncode
            flip("unregister", "fixture")
            print("PASS: five Git worktrees, six concurrent discoveries, 10 stable assigned ports, directory-independent commands, preview isolation, detached project startup/restart/cleanup, occupied ports, stale registration.")
        finally:
            # The cleanup function is defined before a supervisor can be created.
            if supervisor is not None:
                stop_supervisor()
            for sock in (public, control, occupied):
                sock.close()


if __name__ == "__main__":
    main()
