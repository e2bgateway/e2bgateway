/**
 * Write and read a text file with the gateway REST API.
 * Backend: mock, or a backend that supports sandbox text files.
 * Run: node examples/typescript/dist/typescript/filesystem.js
 * Expected: The original file contents and sandbox cleanup.
 */

import { request, withSandbox } from "../javascript/_rest.js";

async function main(): Promise<void> {
  await withSandbox(async (path: string) => {
    const filename = "/tmp/gateway example & test.txt";
    const text = "Hello from E2BGateway!";
    await request("POST", `${path}/files`, 204, { path: filename, content: text });
    const query = new URLSearchParams({ path: filename });
    const result: unknown = await request("GET", `${path}/files?${query}`, 200);
    if (result !== text) {
      throw new Error(`file contents mismatch: ${JSON.stringify(result)}`);
    }
    console.log(`Read back: ${result}`);
  });
}

main().catch((error: unknown) => {
  console.error(error);
  process.exitCode = 1;
});
