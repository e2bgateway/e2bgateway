"""Hello World - Create a sandbox and run a simple command.

This example demonstrates the most basic E2B SDK usage:
1. Create a new sandbox
2. Run a command
3. Print the output
4. Kill the sandbox

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python hello_world.py
"""

import os
from e2b import Sandbox


def main():
    # Connect to E2BGateway
    sandbox = Sandbox.create(
        template="base",
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # Run a simple command
        result = sandbox.commands.run("echo 'Hello from E2BGateway!'")
        print(f"Output: {result.stdout.strip()}")

        # Get sandbox info
        print(f"Sandbox ID: {sandbox.sandbox_id}")
    finally:
        # Clean up
        sandbox.kill()
        print("Sandbox killed.")


if __name__ == "__main__":
    main()
