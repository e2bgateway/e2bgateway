"""Create a template, wait until it is ready, and attach an alias and tag.

Backend: mock or OpenSandbox with template creation, aliases and tags.
Run: python examples/python/custom_template.py
Expected: Build ready and Tag: example, followed by deletion of the template.
"""

import time
import uuid

from _rest import request, segment


def main():
    name = "py-example-" + uuid.uuid4().hex[:12]
    build = request("POST", "/templates", 202, {"name": name, "dockerfile": "FROM python:3.11-slim"})
    path = "/templates/" + segment(build["templateID"])
    try:
        build_id = build["buildID"]
        deadline = time.monotonic() + 60
        while True:
            result = request("POST", f"{path}/builds/{segment(build_id)}/status", 200)
            if result["status"] == "ready":
                print(f"Build ready: {build_id}")
                break
            if result["status"] == "error":
                raise RuntimeError(f"template build failed: {result}")
            if time.monotonic() >= deadline:
                raise TimeoutError("template build did not become ready")
            time.sleep(1)

        request("POST", path + "/aliases", 201, {"alias": name + "-alias"})
        request("POST", path + "/tags", 201, {"name": "example", "buildID": build_id})
        tags = request("GET", path + "/tags", 200)
        if not any(tag["name"] == "example" and tag["buildID"] == build_id for tag in tags):
            raise RuntimeError(f"missing build tag: {tags}")
        print(f"Tag: example ({build_id})")
    finally:
        request("DELETE", path, 204)
        print("Template deleted.")


if __name__ == "__main__":
    main()
