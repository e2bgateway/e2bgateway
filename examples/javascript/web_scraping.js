/**
 * Web Scraping - Fetch and parse web content in a sandbox.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node web_scraping.js
 *
 * Backend: agent-sandbox or OpenSandbox with a Python Jupyter kernel and HTTPS egress.
 * Run: node examples/javascript/web_scraping.js
 * Expected: An HTTP status, parsed headings, and structured results.
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  async function runStep(label, code) {
    console.log(label);
    const execution = await sandbox.runCode(code);
    if (execution.error) {
      throw new Error(`${label} ${execution.error.name}: ${execution.error.value}`);
    }
    for (const log of execution.logs.stdout) {
      console.log(`   ${log}`);
    }
  }

  try {
    // 1. Install dependencies
    await runStep("1. Installing dependencies...", `
import subprocess
subprocess.run(["pip", "install", "requests", "beautifulsoup4"], check=True)
print("Dependencies installed")
`);

    // 2. Fetch a web page
    await runStep("2. Fetching web page...", `
import requests
response = requests.get("https://httpbin.org/html", timeout=10)
response.raise_for_status()
print(f"Status: {response.status_code}")
print(f"Content length: {len(response.text)}")
`);

    // 3. Parse HTML
    await runStep("3. Parsing HTML content...", `
from bs4 import BeautifulSoup

soup = BeautifulSoup(response.text, "html.parser")

headings = soup.find_all(["h1", "h2", "h3"])
for h in headings:
    print(f"  {h.name}: {h.get_text(strip=True)}")

paragraphs = soup.find_all("p")
print(f"\\nFound {len(paragraphs)} paragraphs")
`);

    // 4. Extract structured data
    await runStep("4. Extracting structured data...", `
import json
data = {
    "headings": [h.get_text(strip=True) for h in soup.find_all(["h1", "h2", "h3"])],
    "paragraph_count": len(paragraphs),
    "links": [a.get("href") for a in soup.find_all("a", href=True)],
}
print(json.dumps(data, indent=2))
`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
