// Check the REST response before displaying output from the sandbox.
export function printOutput(result: unknown, marker: string): void {
  if (typeof result !== "object" || result === null ||
      !("exitCode" in result) || result.exitCode !== 0 ||
      ("error" in result && result.error) ||
      !("stdout" in result) || typeof result.stdout !== "string" ||
      !result.stdout.includes(marker)) {
    throw new Error(`unexpected execution result: ${JSON.stringify(result)}`);
  }
  console.log(result.stdout.trim());
}
