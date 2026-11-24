/**
 * Execute Python code with the gateway REST API.
 * Backend: mock, or a backend that supports Python execution.
 * Run: node examples/typescript/dist/typescript/code_execution.js
 * Expected: The execution greeting and sandbox cleanup.
 */

import { request, withSandbox } from "../javascript/_rest.js";
import { printOutput } from "./_output.js";

async function main(): Promise<void> {
  await withSandbox(async (path: string) => {
    const marker = "Hello from code execution!";
    const result: unknown = await request("POST", `${path}/code`, 200, {
      code: `print('${marker}')`,
      language: "python",
    });
    printOutput(result, marker);
  });
}

main().catch((error: unknown) => {
  console.error(error);
  process.exitCode = 1;
});
