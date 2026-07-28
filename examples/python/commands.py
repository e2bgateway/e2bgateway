"""Commands - Run shell commands with streaming output.

Demonstrates command execution:
1. Run simple commands
2. Stream stdout/stderr
3. Set environment variables
4. Run long-running commands
5. Handle process lifecycle

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python commands.py
"""

import os
from e2b import Sandbox


def main():
    sandbox = Sandbox.create(
        template="base",
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Simple command
        print("1. Running simple command...")
        result = sandbox.commands.run("uname -a")
        print(f"   Output: {result.stdout.strip()}")

        # 2. Command with arguments
        print("2. Running command with arguments...")
        result = sandbox.commands.run("ls -la /tmp")
        print(f"   Output:\n{result.stdout}")

        # 3. Environment variables
        print("3. Running command with env vars...")
        sandbox.envs.set({"MY_VAR": "hello", "MY_NUM": "42"})
        result = sandbox.commands.run("echo $MY_VAR $MY_NUM")
        print(f"   Output: {result.stdout.strip()}")

        # 4. Command with working directory
        print("4. Running command in specific directory...")
        sandbox.commands.run("mkdir -p /tmp/workdir")
        result = sandbox.commands.run("pwd", cwd="/tmp/workdir")
        print(f"   Working dir: {result.stdout.strip()}")

        # 5. Streaming output
        print("5. Streaming command output...")
        result = sandbox.commands.run(
            "for i in 1 2 3 4 5; do echo \"Line $i\"; sleep 0.1; done",
            on_stdout=lambda line: print(f"   [stdout] {line}"),
            on_stderr=lambda line: print(f"   [stderr] {line}"),
        )

        # 6. Command with timeout
        print("6. Running command with timeout...")
        result = sandbox.commands.run("echo 'fast command'", timeout=10)
        print(f"   Output: {result.stdout.strip()}")

        # 7. Command exit code
        print("7. Checking exit codes...")
        result = sandbox.commands.run("exit 0")
        print(f"   exit 0 -> exit_code: {result.exit_code}")
        result = sandbox.commands.run("exit 1", check=False)
        print(f"   exit 1 -> exit_code: {result.exit_code}")

        # 8. Process management
        print("8. Starting background process...")
        handle = sandbox.commands.run(
            "echo 'background task done' > /tmp/bg_result.txt",
        )
        print(f"   Process completed with exit code: {handle.exit_code}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
