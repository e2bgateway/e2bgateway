/**
 * Run a greeting command with the gateway REST API.
 * Backend: mock, or a backend that supports sandbox commands.
 * Run: node examples/typescript/dist/typescript/hello_world.js
 * Expected: A greeting and sandbox cleanup.
 */

import { request, withSandbox } from "../javascript/_rest.js";
import { printOutput } from "./_output.js";

async function main(): Promise<void> {
  await withSandbox(async (path: string) => {
    const marker = "Hello from E2BGateway!";
    const result: unknown = await request("POST", `${path}/commands`, 200, {
      command: `echo '${marker}'`,
    });
    printOutput(result, marker);
  });
}

main().catch((error: unknown) => {
  console.error(error);
  process.exitCode = 1;
});
