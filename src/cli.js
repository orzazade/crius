import { readFile } from "node:fs/promises";
import path from "node:path";
import { RunEngine } from "./runner/run-engine.js";

async function main() {
  const specPath = process.argv[2];
  if (!specPath) {
    console.error("Usage: node src/cli.js <run-spec.json>");
    process.exit(1);
  }

  const absolutePath = path.resolve(specPath);
  const input = JSON.parse(await readFile(absolutePath, "utf8"));
  const engine = new RunEngine();
  const { contract, runDir } = await engine.bootstrapRun(input);

  console.log(`Run created: ${contract.runId}`);
  console.log(`Artifacts: ${runDir}`);
  console.log(`Next stage: ${contract.currentStage}`);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});

