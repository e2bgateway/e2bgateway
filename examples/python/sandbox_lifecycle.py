"""Sandbox Lifecycle - Create, pause, resume, set timeout, and kill.

Demonstrates the full sandbox lifecycle:
1. Create a sandbox
2. List running sandboxes
3. Pause the sandbox
4. Resume from paused state
5. Set timeout
6. Kill the sandbox

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python sandbox_lifecycle.py
"""

import os
from e2b import Sandbox


def main():
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    domain = os.environ.get("E2B_DOMAIN", "localhost:8080")

    # 1. Create sandbox
    print("1. Creating sandbox...")
    sandbox = Sandbox.create(
        template="base",
        api_key=api_key,
        domain=domain,
    )
    print(f"   Created: {sandbox.sandbox_id}")

    # 2. List running sandboxes
    print("2. Listing sandboxes...")
    sandboxes = Sandbox.list(api_key=api_key, domain=domain)
    print(f"   Running sandboxes: {len(sandboxes)}")
    for sbx in sandboxes:
        print(f"   - {sbx.sandbox_id} (template: {sbx.template_id})")

    # 3. Pause sandbox
    print("3. Pausing sandbox...")
    paused_id = sandbox.pause()
    print(f"   Paused: {paused_id}")

    # 4. Resume sandbox
    print("4. Resuming sandbox...")
    sandbox = Sandbox.resume(
        paused_id,
        api_key=api_key,
        domain=domain,
    )
    print(f"   Resumed: {sandbox.sandbox_id}")

    # 5. Set timeout
    print("5. Setting timeout to 300 seconds...")
    sandbox.set_timeout(300)
    print("   Timeout set.")

    # 6. Kill sandbox
    print("6. Killing sandbox...")
    sandbox.kill()
    print("   Killed.")


if __name__ == "__main__":
    main()
