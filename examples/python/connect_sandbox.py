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

Backend: an SDK-compatible backend that supports reconnect and file operations.
Run: python examples/python/connect_sandbox.py
Expected: The original sandbox ID and the state read after reconnecting.
"""

import os
from e2b import Sandbox


def main():
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    domain = os.environ.get("E2B_DOMAIN", "localhost:8080")

    sandbox = Sandbox.create(template="base", api_key=api_key, domain=domain)
    try:
        path = "/tmp/state.txt"
        sandbox.files.write(path, "persistent state")

        # Leave the sandbox alive while obtaining a new SDK handle.
        reconnected = Sandbox.connect(sandbox.sandbox_id, api_key=api_key, domain=domain)
        state = reconnected.files.read(path)
        if state != "persistent state":
            raise RuntimeError(f"unexpected sandbox state: {state!r}")
        print(f"Reconnected to {sandbox.sandbox_id}: {state}")
    finally:
        sandbox.kill()
        print("Sandbox killed.")


if __name__ == "__main__":
    main()
