/**
 * Reopen a running sandbox by ID using the gateway REST API.
 *
 * Backend: mock, or any backend exposing sandbox details and text files.
 * Run: node examples/javascript/connect_sandbox.js
 * Expected: The saved contents retrieved by ID without creating a second sandbox.
 */

const { request, withSandbox } = require("./_rest");

async function main() {
  await withSandbox(async (sandboxPath) => {
    const filename = "/tmp/state.txt";
    const expected = "persistent state";
    await request("POST", `${sandboxPath}/files`, 204, { path: filename, content: expected });

    // Keep the sandbox alive and retrieve its state using the ID from the lookup.
    const { sandboxID } = await request("GET", sandboxPath, 200);
    const reconnectedPath = `/sandboxes/${encodeURIComponent(sandboxID)}`;
    if (reconnectedPath !== sandboxPath) {
      throw new Error(`unexpected sandbox ID: ${JSON.stringify(sandboxID)}`);
    }
    const query = new URLSearchParams({ path: filename });
    const state = await request("GET", `${reconnectedPath}/files?${query}`, 200);
    if (state !== expected) {
      throw new Error(`unexpected sandbox state: ${JSON.stringify(state)}`);
    }
    console.log(`Reconnected to ${sandboxID}: ${state}`);
  });
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
