/**
 * List templates exposed by the gateway and inspect the first result.
 * Backend: mock or a backend with template listing and detail support.
 * Run: node examples/javascript/templates.js
 * Expected: Available template IDs and the first template's build ID (if present).
 */

const { request } = require("./_rest");

async function main() {
  const templates = await request("GET", "/templates", 200);
  if (!templates.length) {
    console.log("No templates available.");
    return;
  }
  for (const { templateID } of templates) console.log(templateID);
  const first = await request("GET", `/templates/${encodeURIComponent(templates[0].templateID)}`, 200);
  console.log(`Template: ${first.templateID}; build: ${first.buildID ?? ""}`);
}

main().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
