"""List templates exposed by the gateway and inspect the first result.

Backend: mock or a backend with template listing and detail support.
Run: python examples/python/templates.py
Expected: Available template IDs and the first template's build ID (if present).
"""

from _rest import request, segment


def main():
    templates = request("GET", "/templates", 200)
    if not templates:
        print("No templates available.")
        return
    for template in templates:
        print(template["templateID"])
    first = request("GET", "/templates/" + segment(templates[0]["templateID"]), 200)
    print(f"Template: {first['templateID']}; build: {first.get('buildID', '')}")


if __name__ == "__main__":
    main()
