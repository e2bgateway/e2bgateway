/**
 * Templates - List and manage sandbox templates.
 *
 * Usage:
 *   E2B_DOMAIN=localhost:8080 E2B_API_KEY=test-key node templates.js
 */

const { Sandbox } = require("@e2b/code-interpreter");

async function main() {
  const apiKey = process.env.E2B_API_KEY || "test-key";
  const domain = process.env.E2B_DOMAIN || "localhost:8080";

  // 1. List templates
  console.log("1. Listing templates...");
  const templates = await Sandbox.listTemplates({ apiKey, domain });
  for (const tmpl of templates) {
    console.log(
      `   - ${tmpl.templateId} (public: ${tmpl.public}, ready: ${tmpl.ready})`
    );
  }

  // 2. Create sandbox from template
  console.log("2. Creating sandbox from template...");
  const sandbox = await Sandbox.create({
    apiKey,
    domain,
    template: "base",
  });
  console.log(`   Sandbox: ${sandbox.sandboxId}`);

  // 3. Run verification
  console.log("3. Running verification code...");
  const result = await sandbox.commands.run("echo 'Template works!'");
  console.log(`   Output: ${result.stdout.trim()}`);

  await sandbox.kill();
  console.log("\nDone.");
}

main().catch(console.error);
