/**
 * Create a template, wait until it is ready, and attach an alias and tag.
 * Backend: mock or OpenSandbox with template creation, aliases and tags.
 * Run: node examples/javascript/custom_template.js
 * Expected: Build ready and Tag: example, followed by deletion of the template.
 */

const { randomUUID } = require("node:crypto");
const { performance } = require("node:perf_hooks");
const { setTimeout: sleep } = require("node:timers/promises");
const { request } = require("./_rest");

async function main() {
  const name = `js-example-${randomUUID().slice(0, 12)}`;
  const { templateID, buildID } = await request("POST", "/templates", 202, {
    name, dockerfile: "FROM python:3.11-slim",
  });
  if (!templateID) throw new Error("missing template ID");
  const path = `/templates/${encodeURIComponent(templateID)}`;
  try {
    if (!buildID) throw new Error("missing build ID");
    const deadline = performance.now() + 60_000;
    for (;;) {
      const result = await request("POST", `${path}/builds/${encodeURIComponent(buildID)}/status`, 200);
      if (result.status === "ready") {
        console.log(`Build ready: ${buildID}`);
        break;
      }
      if (result.status === "error") throw new Error(`template build failed: ${JSON.stringify(result)}`);
      if (performance.now() >= deadline) throw new Error("template build did not become ready");
      await sleep(1_000);
    }

    await request("POST", `${path}/aliases`, 201, { alias: `${name}-alias` });
    await request("POST", `${path}/tags`, 201, { name: "example", buildID });
    const tags = await request("GET", `${path}/tags`, 200);
    if (!tags.some((tag) => tag.name === "example" && tag.buildID === buildID)) {
      throw new Error(`missing build tag: ${JSON.stringify(tags)}`);
    }
    console.log(`Tag: example (${buildID})`);
  } finally {
    await request("DELETE", path, 204);
    console.log("Template deleted.");
  }
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
