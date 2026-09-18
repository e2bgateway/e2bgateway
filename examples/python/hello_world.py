"""Hello World - Create a sandbox and run a simple command.

This example demonstrates the most basic E2B SDK usage:
1. Create a new sandbox
2. Run a command
3. Print output
4. Kill the sandbox

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python hello_world.py
"""

import os
import ssl
from e2b import Sandbox


def main():
    # For local testing with self-signed certs, disable SSL verification
    # In production, use proper certificates
    if os.environ.get("E2B_SKIP_SSL_VERIFY"):
        # Set environment variable for pyqwest to skip SSL verification
        os.environ["PYQWEST_TLS_VERIFY"] = "false"

        # Also patch ssl module for httpx
        _original_create_default_context = ssl.create_default_context
        def _patched_create_default_context():
            ctx = _original_create_default_context()
            ctx.check_hostname = False
            ctx.verify_mode = ssl.CERT_NONE
            return ctx
        ssl.create_default_context = _patched_create_default_context

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
