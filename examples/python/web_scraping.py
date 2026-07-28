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
"""

import os
from e2b_code_interpreter import Sandbox


def main():
    sandbox = Sandbox.create(
        api_key=os.environ.get("E2B_API_KEY", "test-key"),
        domain=os.environ.get("E2B_DOMAIN", "localhost:8080"),
    )

    try:
        # 1. Install dependencies
        print("1. Installing dependencies...")
        sandbox.run_code("""
import subprocess
subprocess.run(["pip", "install", "requests", "beautifulsoup4"], capture_output=True)
print("Dependencies installed")
""")

        # 2. Fetch a web page
        print("2. Fetching web page...")
        execution = sandbox.run_code("""
import requests

response = requests.get("https://httpbin.org/html")
print(f"Status: {response.status_code}")
print(f"Content length: {len(response.text)}")
""")
        for log in execution.logs:
            print(f"   {log}")

        # 3. Parse HTML
        print("3. Parsing HTML content...")
        execution = sandbox.run_code("""
from bs4 import BeautifulSoup
import requests

response = requests.get("https://httpbin.org/html")
soup = BeautifulSoup(response.text, "html.parser")

# Extract headings
headings = soup.find_all(["h1", "h2", "h3"])
for h in headings:
    print(f"  {h.name}: {h.get_text(strip=True)}")

# Extract paragraphs
paragraphs = soup.find_all("p")
print(f"\\nFound {len(paragraphs)} paragraphs")
""")
        for log in execution.logs:
            print(f"   {log}")

        # 4. Extract structured data
        print("4. Extracting structured data...")
        execution = sandbox.run_code("""
import json

data = {
    "headings": [h.get_text(strip=True) for h in soup.find_all(["h1", "h2", "h3"])],
    "paragraph_count": len(paragraphs),
    "links": [a.get("href") for a in soup.find_all("a", href=True)],
}
print(json.dumps(data, indent=2))
""")
        for log in execution.logs:
            print(f"   {log}")

    finally:
        sandbox.kill()
        print("\nSandbox killed.")


if __name__ == "__main__":
    main()
