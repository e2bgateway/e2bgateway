"""Cloud Browser - Expose sandbox ports and run web services.

Demonstrates port exposure and web service patterns:
1. Create a sandbox
2. Start a web server inside
3. Get the public URL for the port
4. Make requests to the exposed service

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python cloud_browser.py
"""

import os
import requests
from e2b import Sandbox


def main():
    sandbox = Sandbox.create(
        template="base",
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Install web framework
        print("1. Installing Flask...")
        sandbox.commands.run("pip install flask")

        # 2. Write a web app
        print("2. Writing web app...")
        sandbox.files.write("/tmp/app.py", """
from flask import Flask, jsonify
app = Flask(__name__)

@app.route('/')
def index():
    return jsonify({"message": "Hello from sandbox!", "status": "running"})

@app.route('/health')
def health():
    return jsonify({"status": "healthy"})

if __name__ == '__main__':
    app.run(host='0.0.0.0', port=3000)
""")

        # 3. Start the web server in background
        print("3. Starting web server on port 3000...")
        sandbox.commands.run("python3 /tmp/app.py &")

        # 4. Get the public URL
        print("4. Getting public URL...")
        try:
            host = sandbox.get_host(3000)
            print(f"   Public URL: {host}")

            # 5. Make a request to the exposed service
            print("5. Making request to exposed service...")
            resp = requests.get(f"http://{host}/")
            print(f"   Response: {resp.json()}")
        except Exception as e:
            print(f"   Port exposure not available: {e}")
            # Fallback: test via command
            result = sandbox.commands.run("curl -s http://localhost:3000/ 2>/dev/null || echo 'server not ready'")
            print(f"   Local test: {result.stdout.strip()}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
