"""Reopen a running sandbox by ID using the gateway REST API.

Backend: mock, or any backend exposing sandbox details and text files.
Run: python examples/python/connect_sandbox.py
Expected: The saved contents retrieved by ID without creating a second sandbox.
"""

from urllib.parse import urlencode

from _rest import request, sandbox, segment


def main():
    with sandbox() as sandbox_path:
        filename = "/tmp/state.txt"
        expected = "persistent state"
        request("POST", sandbox_path + "/files", 204, {"path": filename, "content": expected})

        # Keep the sandbox alive and retrieve its state using the ID from the lookup.
        details = request("GET", sandbox_path, 200)
        sandbox_id = details["sandboxID"]
        reconnected_path = "/sandboxes/" + segment(sandbox_id)
        if reconnected_path != sandbox_path:
            raise RuntimeError(f"unexpected sandbox ID: {sandbox_id!r}")
        query = urlencode({"path": filename})
        state = request("GET", f"{reconnected_path}/files?{query}", 200)
        if state != expected:
            raise RuntimeError(f"unexpected sandbox state: {state!r}")
        print(f"Reconnected to {sandbox_id}: {state}")


if __name__ == "__main__":
    main()
