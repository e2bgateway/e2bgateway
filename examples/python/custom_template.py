"""Custom Template - Build and use custom sandbox templates.

Demonstrates template building workflow:
1. List existing templates
2. Create a template with custom configuration
3. Build the template
4. Check build status
5. Create sandbox from custom template

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python custom_template.py
"""

import os
import time
import requests


def main():
    base_url = os.environ.get("E2B_DOMAIN", "localhost:8080")
    if not base_url.startswith("http"):
        base_url = f"http://{base_url}"
    api_key = os.environ.get("E2B_API_KEY", "test-key")
    headers = {"X-API-Key": api_key, "Content-Type": "application/json"}

    # 1. List existing templates
    print("1. Listing templates...")
    resp = requests.get(f"{base_url}/templates", headers=headers)
    templates = resp.json()
    for t in templates:
        print(f"   - {t.get('templateId')} (ready: {t.get('ready')})")

    # 2. Create a new template (trigger build)
    print("2. Creating template...")
    resp = requests.post(f"{base_url}/templates", headers=headers, json={
        "name": "custom-python",
        "dockerfile": "FROM python:3.11-slim\nRUN pip install numpy pandas",
    })
    build = resp.json()
    template_id = build.get("templateId", "")
    build_id = build.get("buildId", "")
    print(f"   Template: {template_id}")
    print(f"   Build: {build_id}")
    print(f"   Status: {build.get('status')}")

    # 3. Check build status
    print("3. Checking build status...")
    resp = requests.post(
        f"{base_url}/templates/{template_id}/builds/{build_id}/status",
        headers=headers,
    )
    status = resp.json()
    print(f"   Status: {status.get('status')}")

    # 4. Create alias
    print("4. Creating template alias...")
    resp = requests.post(
        f"{base_url}/templates/{template_id}/aliases",
        headers=headers,
        json={"alias": "latest"},
    )
    print(f"   Alias created: {resp.status_code}")

    # 5. Create tag
    print("5. Creating template tag...")
    resp = requests.post(
        f"{base_url}/templates/{template_id}/tags",
        headers=headers,
        json={"name": "v1.0", "buildID": build_id},
    )
    print(f"   Tag created: {resp.status_code}")

    # 6. List tags
    print("6. Listing tags...")
    resp = requests.get(f"{base_url}/templates/{template_id}/tags", headers=headers)
    tags = resp.json()
    for tag in tags:
        print(f"   - {tag.get('name')} -> {tag.get('buildID')}")

    print("\nDone.")


if __name__ == "__main__":
    main()
