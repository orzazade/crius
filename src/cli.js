import { readFile } from "node:fs/promises";
import path from "node:path";
import { RunEngine } from "./runner/run-engine.js";
import { ProjectImprover } from "./improver/project-improver.js";

function readOption(argv, name, fallback) {
  const index = argv.indexOf(name);
  if (index === -1) return fallback;
  const value = argv[index + 1];
  if (!value || value.startsWith("--")) {
    throw new Error(`Missing value for ${name}`);
  }
  return value;
}

async function bootstrapFromJson(specPath) {
  const absolutePath = path.resolve(specPath);
  const input = JSON.parse(await readFile(absolutePath, "utf8"));
  const engine = new RunEngine();
  const { contract, runDir } = await engine.bootstrapRun(input);

  console.log(`Run created: ${contract.runId}`);
  console.log(`Artifacts: ${runDir}`);
  console.log(`Next stage: ${contract.currentStage}`);
}

async function initializeImproveProject(argv) {
  const repositoryPath = argv[1];
  if (!repositoryPath) {
    throw new Error(
      "Usage: node src/cli.js improve-project <repository-path> [--goal \"...\"] [--provider codex] [--evaluator claude] [--base main]"
    );
  }

  const improver = new ProjectImprover();
  const result = await improver.initializeRun({
    repositoryPath: path.resolve(repositoryPath),
    goal: readOption(argv, "--goal", undefined),
    implementationProvider: readOption(argv, "--provider", "codex"),
    evaluatorProvider: readOption(argv, "--evaluator", "claude"),
    baseRef: readOption(argv, "--base", undefined)
  });

  console.log(`Improve-project run created: ${result.contract.runId}`);
  console.log(`Artifacts: ${result.runDir}`);
  console.log(`Worktree: ${result.worktree.worktreePath}`);
  console.log(`Branch: ${result.worktree.branchName}`);
  console.log(`Implementer: ${result.providers.implementation.name}`);
  console.log(`Evaluator: ${result.providers.evaluator.name}`);
}

async function executeImproveRun(argv) {
  const runId = argv[1];
  if (!runId) {
    throw new Error(
      "Usage: node src/cli.js execute-run <run-id> [--max-revisions 2]"
    );
  }

  const maxRevisions = Number(readOption(argv, "--max-revisions", "2"));
  const improver = new ProjectImprover();
  const result = await improver.executeRun(runId, { maxRevisions });

  console.log(`Run: ${result.runId}`);
  console.log(`Status: ${result.status}`);
  if (result.revisionCount != null) {
    console.log(`Revision count: ${result.revisionCount}`);
  }
}

async function executeSingleStage(argv) {
  const runId = argv[1];
  const stage = argv[2];
  if (!runId || !stage) {
    throw new Error(
      "Usage: node src/cli.js execute-stage <run-id> <planning|implementation|evaluation>"
    );
  }

  const improver = new ProjectImprover();
  const result = await improver.executeStage(runId, stage);

  console.log(`Run: ${runId}`);
  console.log(`Stage: ${stage}`);
  console.log(`Provider: ${result.provider}`);
  console.log(`Response: ${result.responsePath}`);
}

function printHelp() {
  console.error(
    "Usage:\n"
      + "  node src/cli.js <run-spec.json>\n"
      + "  node src/cli.js bootstrap <run-spec.json>\n"
      + "  node src/cli.js improve-project <repository-path> [--goal \"...\"] [--provider codex] [--evaluator claude] [--base main]\n"
      + "  node src/cli.js execute-stage <run-id> <planning|implementation|evaluation>\n"
      + "  node src/cli.js execute-run <run-id> [--max-revisions 2]\n"
  );
}

async function main() {
  const argv = process.argv.slice(2);
  if (argv.length === 0 || argv[0] === "--help" || argv[0] === "-h" || argv[0] === "help") {
    printHelp();
    process.exit(1);
  }

  if (argv[0] === "bootstrap") {
    await bootstrapFromJson(argv[1]);
    return;
  }

  if (argv[0] === "improve-project") {
    await initializeImproveProject(argv);
    return;
  }

  if (argv[0] === "execute-run") {
    await executeImproveRun(argv);
    return;
  }

  if (argv[0] === "execute-stage") {
    await executeSingleStage(argv);
    return;
  }

  await bootstrapFromJson(argv[0]);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : String(error));
  process.exit(1);
});
