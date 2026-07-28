/**
 * Cloud Browser - Expose sandbox ports and run web services.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node cloud_browser.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const sandbox = await Sandbox.create({
    apiKey: process.env.E2B_API_KEY || "test-key",
    domain: process.env.E2B_DOMAIN || "localhost:8080",
  });

  try {
    // 1. Write a simple HTTP server
    console.log("1. Writing HTTP server...");
    await sandbox.files.write(
      "/tmp/server.js",
      `
const http = require('http');
const server = http.createServer((req, res) => {
  res.writeHead(200, {'Content-Type': 'application/json'});
  res.end(JSON.stringify({message: 'Hello from sandbox!', status: 'running'}));
});
server.listen(3000, '0.0.0.0');
console.log('Server running on port 3000');
`
    );

    // 2. Start the server
    console.log("2. Starting HTTP server on port 3000...");
    await sandbox.commands.run("node /tmp/server.js &");

    // 3. Get public URL
    console.log("3. Getting public URL...");
    try {
      const host = sandbox.getHost(3000);
      console.log(`   Public URL: ${host}`);
    } catch (e) {
      console.log(`   Port exposure not available: ${e.message}`);
    }

    // 4. Test locally
    console.log("4. Testing locally...");
    const result = await sandbox.commands.run(
      "curl -s http://localhost:3000/ 2>/dev/null || echo 'server not ready'"
    );
    console.log(`   Response: ${result.stdout.trim()}`);
  } finally {
    await sandbox.kill();
    console.log("\nSandbox killed.");
  }
}

main().catch(console.error);
