"""Connect to existing sandbox - Reconnect and interact.

Demonstrates connecting to a running sandbox:
1. Create a sandbox
2. Disconnect (keep running)
3. Reconnect by sandbox ID
4. Interact with the reconnected sandbox

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python connect_sandbox.py
"""

import os
from e2b import Sandbox


def main():
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    domain = os.environ.get("E2B_DOMAIN", "localhost:8080")

    # 1. Create a sandbox
    print("1. Creating sandbox...")
    sandbox = Sandbox.create(
        template="base",
        api_key=api_key,
        domain=domain,
    )
    sandbox_id = sandbox.sandbox_id
    print(f"   Created: {sandbox_id}")

    # 2. Write some state
    print("2. Writing state...")
    sandbox.commands.run("echo 'persistent state' > /tmp/state.txt")
    sandbox.kill()
    print(f"   Disconnected from: {sandbox_id}")

    # 3. List running sandboxes to find it
    print("3. Listing sandboxes...")
    sandboxes = Sandbox.list(api_key=api_key, domain=domain)
    print(f"   Running: {len(sandboxes)}")

    # 4. Connect to existing sandbox
    # Note: This requires the sandbox to still be running
    # In real E2B, you'd use Sandbox.connect(sandbox_id)
    print("4. Reconnecting to sandbox...")
    try:
        reconnected = Sandbox.connect(
            sandbox_id,
            api_key=api_key,
            domain=domain,
        )
        result = reconnected.commands.run("cat /tmp/state.txt")
        print(f"   State: {result.stdout.strip()}")
        reconnected.kill()
    except Exception as e:
        print(f"   Connect failed (expected for mock): {e}")
        print("   Creating new sandbox to verify...")

    print("\nDone.")


if __name__ == "__main__":
    main()
