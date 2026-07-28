"""Filesystem Operations - File CRUD in a sandbox.

Demonstrates filesystem operations:
1. Write files
2. Read files
3. List directory contents
4. Create directories
5. Upload/download files
6. Move files
7. Remove files

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python filesystem.py
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
        # 1. Write a file
        print("1. Writing file...")
        sandbox.files.write("/tmp/hello.txt", "Hello from E2BGateway!")
        print("   Written: /tmp/hello.txt")

        # 2. Read a file
        print("2. Reading file...")
        content = sandbox.files.read("/tmp/hello.txt")
        print(f"   Content: {content}")

        # 3. List directory
        print("3. Listing /tmp directory...")
        entries = sandbox.files.list("/tmp")
        for entry in entries:
            print(f"   - {entry.name} ({'dir' if entry.is_dir else 'file'})")

        # 4. Create directory
        print("4. Creating directory...")
        sandbox.files.make_dir("/tmp/my_project")
        print("   Created: /tmp/my_project")

        # 5. Write multiple files
        print("5. Writing multiple files...")
        sandbox.files.write("/tmp/my_project/main.py", "print('hello')")
        sandbox.files.write("/tmp/my_project/config.json", '{"key": "value"}')
        entries = sandbox.files.list("/tmp/my_project")
        for entry in entries:
            print(f"   - {entry.name}")

        # 6. Move/rename file
        print("6. Moving file...")
        sandbox.files.move("/tmp/my_project/main.py", "/tmp/my_project/app.py")
        entries = sandbox.files.list("/tmp/my_project")
        print(f"   Files after move: {[e.name for e in entries]}")

        # 7. Upload binary file
        print("7. Uploading binary file...")
        data = b"\x89PNG\r\n\x1a\n" + b"\x00" * 100  # Fake PNG header
        sandbox.files.write("/tmp/image.png", data)
        print("   Uploaded: /tmp/image.png")

        # 8. Download file as bytes
        print("8. Downloading file...")
        raw = sandbox.files.read("/tmp/image.png", format="bytes")
        print(f"   Downloaded {len(raw)} bytes")

        # 9. Remove files
        print("9. Removing files...")
        sandbox.files.remove("/tmp/my_project/app.py")
        sandbox.files.remove("/tmp/my_project/config.json")
        print("   Removed files from /tmp/my_project")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
