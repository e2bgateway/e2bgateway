#!/usr/bin/env python3
"""Verify example parity and run the REST smoke cases on a local mock gateway."""

import os
import socket
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.error import URLError
from urllib.request import ProxyHandler, build_opener

ROOT = Path(__file__).resolve().parents[1]
EXAMPLES = ROOT / "examples"
HOST, PORT = "127.0.0.1", 18080  # configs/e2bgateway-test.yaml
ENDPOINT = f"http://{HOST}:{PORT}"
SCENARIOS = {
    "ai_code_interpreter", "cloud_browser", "code_execution", "coding_agent",
    "commands", "connect_sandbox", "custom_template", "data_analysis", "filesystem",
    "hello_world", "sandbox_lifecycle", "templates", "warm_pools", "web_scraping",
}
CORE = {
    "hello_world": "Hello from E2BGateway!",
    "code_execution": "Hello from code execution!",
    "filesystem": "Read back: Hello from E2BGateway!",
}


def check_parity():
    entries = {
        "Go": {path.parent.name: path for path in (EXAMPLES / "go").glob("*/main.go")},
        "Python": {path.stem: path for path in (EXAMPLES / "python").glob("*.py") if not path.stem.startswith("_")},
        "JavaScript": {path.stem: path for path in (EXAMPLES / "javascript").glob("*.js") if not path.stem.startswith("_")},
    }
    for language, files in entries.items():
        names = files.keys()
        if names != SCENARIOS:
            raise RuntimeError(f"{language} example matrix: missing {sorted(SCENARIOS - names)}, extra {sorted(names - SCENARIOS)}")
        for path in files.values():
            header = path.read_text(encoding="utf-8").splitlines()[:50]
            for field in ("Backend:", "Run:", "Expected:"):
                if not any(line.lstrip(" /*").startswith(field) for line in header):
                    raise RuntimeError(f"{path.relative_to(ROOT)} is missing {field} in its header")



def wait_ready(gateway):
    opener = build_opener(ProxyHandler({}))
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if gateway.poll() is not None:
            raise RuntimeError(f"gateway quit during startup ({gateway.returncode})")
        try:
            with opener.open(f"{ENDPOINT}/readyz", timeout=1) as response:
                if response.status == 200:
                    return
        except (URLError, TimeoutError):
            pass
        time.sleep(0.2)
    raise TimeoutError("mock gateway did not become ready")


def run_examples(env):
    for scenario, marker in {**CORE, "custom_template": "Tag: example", "templates": "Template: "}.items():
        for command in (
            ["go", "run", f"./examples/go/{scenario}"],
            [sys.executable, f"examples/python/{scenario}.py"],
            ["node", f"examples/javascript/{scenario}.js"],
        ):
            if scenario in CORE and command[0] != "go":
                command.append("--rest")
            run_example(command, marker, env)
    run_example(["go", "run", "./examples/go/code_execution", "--stream"], CORE["code_execution"], env)
    run_example(["go", "run", "./examples/go/connect_sandbox"], "persistent state", env)
    run_example(["go", "run", "./examples/go/sandbox_lifecycle"], "Resumed.", env)


def run_example(command, marker, env):
    print(f"\n$ {' '.join(command)}", flush=True)
    result = subprocess.run(command, cwd=ROOT, env=env, check=False, timeout=120,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, encoding="utf-8", errors="replace")
    print(result.stdout, end="", flush=True)
    if result.returncode or marker not in result.stdout:
        raise RuntimeError(f"smoke failed (exit {result.returncode}); expected output containing {marker!r}")


def main():
    sys.stdout.reconfigure(errors="backslashreplace")
    sys.stderr.reconfigure(errors="backslashreplace")
    check_parity()
    env = {**os.environ, "E2B_API_URL": ENDPOINT, "E2B_DOMAIN": f"{HOST}:{PORT}",
           "E2B_API_KEY": "test-key", "PYTHONDONTWRITEBYTECODE": "1", "PYTHONUTF8": "1"}
    with tempfile.TemporaryDirectory(prefix="e2bgateway-smoke-") as temp:
        binary = Path(temp) / ("e2bgateway.exe" if os.name == "nt" else "e2bgateway")
        subprocess.run(["go", "build", "-o", str(binary), "./cmd/e2bgateway"], cwd=ROOT, check=True, timeout=300)
        # Refuse to run against an unrelated service already listening on the smoke port.
        with socket.socket() as probe:
            probe.settimeout(1)
            if probe.connect_ex((HOST, PORT)) == 0:
                raise RuntimeError(f"smoke port {PORT} is already in use")
        log = Path(temp) / "gateway.log"
        with log.open("wb") as output:
            gateway = subprocess.Popen([str(binary), "serve", "--config", "configs/e2bgateway-test.yaml"],
                                       cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT,
                                       creationflags=subprocess.CREATE_NO_WINDOW if os.name == "nt" else 0)
            try:
                wait_ready(gateway)
                run_examples(env)
            except Exception:
                print(log.read_text(encoding="utf-8", errors="replace"), file=sys.stderr)
                raise
            finally:
                if gateway.poll() is None:
                    gateway.terminate()
                try:
                    gateway.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    gateway.kill()
                    gateway.wait()
    print("\nExample matrix and mock smoke checks passed.")


if __name__ == "__main__":
    main()
