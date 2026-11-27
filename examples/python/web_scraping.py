"""Web Scraping - Fetch and parse web content in a sandbox.

Demonstrates web scraping capabilities:
1. Install scraping dependencies
2. Fetch web pages
3. Parse HTML content
4. Extract structured data

Usage:
    export E2B_DOMAIN=localhost:8080
    export E2B_API_KEY=your-api-key
    python web_scraping.py

Backend: agent-sandbox or OpenSandbox with a Python Jupyter kernel and HTTPS egress.
Run: python examples/python/web_scraping.py
Expected: An HTTP status, parsed headings, and structured results.
"""

import os
from e2b_code_interpreter import Sandbox


def main():
    sandbox = Sandbox.create(
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    def run_step(label, code):
        print(label)
        execution = sandbox.run_code(code)
        if execution.error:
            raise RuntimeError(f"{label} {execution.error.name}: {execution.error.value}")
        for log in execution.logs.stdout:
            print(f"   {log}")

    try:
        # 1. Install dependencies
        run_step("1. Installing dependencies...", """
import subprocess
subprocess.run(["pip", "install", "requests", "beautifulsoup4"], check=True)
print("Dependencies installed")
""")

        # 2. Fetch a web page
        run_step("2. Fetching web page...", """
import requests

response = requests.get("https://httpbin.org/html", timeout=10)
response.raise_for_status()
print(f"Status: {response.status_code}")
print(f"Content length: {len(response.text)}")
""")

        # 3. Parse HTML
        run_step("3. Parsing HTML content...", """
from bs4 import BeautifulSoup

soup = BeautifulSoup(response.text, "html.parser")

# Extract headings
headings = soup.find_all(["h1", "h2", "h3"])
for h in headings:
    print(f"  {h.name}: {h.get_text(strip=True)}")

# Extract paragraphs
paragraphs = soup.find_all("p")
print(f"\\nFound {len(paragraphs)} paragraphs")
""")

        # 4. Extract structured data
        run_step("4. Extracting structured data...", """
import json

data = {
    "headings": [h.get_text(strip=True) for h in soup.find_all(["h1", "h2", "h3"])],
    "paragraph_count": len(paragraphs),
    "links": [a.get("href") for a in soup.find_all("a", href=True)],
}
print(json.dumps(data, indent=2))
""")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
