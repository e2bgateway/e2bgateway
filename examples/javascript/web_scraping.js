/**
 * Web Scraping - Fetch and parse web content in a sandbox.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node web_scraping.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Install dependencies
    console.log("1. Installing dependencies...");
    await sandbox.runCode(`
import subprocess
subprocess.run(["pip", "install", "requests", "beautifulsoup4"], capture_output=True)
print("Dependencies installed")
`);

    // 2. Fetch a web page
    console.log("2. Fetching web page...");
    const exec1 = await sandbox.runCode(`
import requests
response = requests.get("https://httpbin.org/html")
print(f"Status: {response.status_code}")
print(f"Content length: {len(response.text)}")
`);
    for (const log of exec1.logs) {
      console.log(`   ${log}`);
    }

    // 3. Parse HTML
    console.log("3. Parsing HTML content...");
    const exec2 = await sandbox.runCode(`
from bs4 import BeautifulSoup
import requests

response = requests.get("https://httpbin.org/html")
soup = BeautifulSoup(response.text, "html.parser")

headings = soup.find_all(["h1", "h2", "h3"])
for h in headings:
    print(f"  {h.name}: {h.get_text(strip=True)}")

paragraphs = soup.find_all("p")
print(f"\\nFound {len(paragraphs)} paragraphs")
`);
    for (const log of exec2.logs) {
      console.log(`   ${log}`);
    }

    // 4. Extract structured data
    console.log("4. Extracting structured data...");
    const exec3 = await sandbox.runCode(`
import json
data = {
    "headings": [h.get_text(strip=True) for h in soup.find_all(["h1", "h2", "h3"])],
    "paragraph_count": len(paragraphs),
    "links": [a.get("href") for a in soup.find_all("a", href=True)],
}
print(json.dumps(data, indent=2))
`);
    for (const log of exec3.logs) {
      console.log(`   ${log}`);
    }
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
