"""Template Management - Create, list, and manage sandbox templates.

Demonstrates template operations:
1. List available templates
2. Create a new template from Dockerfile
3. Check build status
4. Create template aliases
5. Manage template tags

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python templates.py
"""

import os
from e2b import Sandbox


def main():
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    domain = os.environ.get("E2B_DOMAIN", "localhost:8080")

    # 1. List templates
    print("1. Listing templates...")
    templates = Sandbox.list_templates(api_key=api_key, domain=domain)
    for tmpl in templates:
        print(f"   - {tmpl.template_id} (public: {tmpl.public}, ready: {tmpl.ready})")

    # 2. Create sandbox from template
    print("2. Creating sandbox from template...")
    sandbox = Sandbox.create(
        template="base",
        api_key=api_key,
        domain=domain,
    )
    print(f"   Sandbox: {sandbox.sandbox_id}")

    # 3. Run code in the sandbox
    print("3. Running verification code...")
    result = sandbox.commands.run("echo 'Template works!'")
    print(f"   Output: {result.stdout.strip()}")

    # 4. List template tags (via REST API)
    print("4. Template operations via REST API...")
    import requests
    base_url = f"http://{domain}"
    headers = {"X-API-Key": api_key}

    # List tags
    resp = requests.get(f"{base_url}/templates/base/tags", headers=headers)
    if resp.status_code == 200:
        tags = resp.json()
        print(f"   Tags for 'base': {tags}")
    else:
        print(f"   Tags listing: {resp.status_code}")

    sandbox.kill()
    print("\nDone.")


if __name__ == "__main__":
    main()
