import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, realpath, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { ProjectImprover } from "../src/improver/project-improver.js";

function git(cwd, args) {
  return execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8" }).trim();
}

async function createTempRepository() {
  const root = await mkdtemp(path.join(os.tmpdir(), "crius-improver-repo-"));
  git(root, ["init", "-b", "main"]);
  git(root, ["config", "user.name", "Crius Test"]);
  git(root, ["config", "user.email", "crius@example.com"]);
  await writeFile(path.join(root, "README.md"), "# Temp Repo\n", "utf8");
  git(root, ["add", "README.md"]);
  git(root, ["commit", "-m", "init"]);
  return root;
}

test("ProjectImprover initializes an isolated improve-project run", async () => {
  const repositoryPath = await createTempRepository();
  const artifactRoot = await mkdtemp(path.join(os.tmpdir(), "crius-improver-artifacts-"));
  const worktreeRoot = await mkdtemp(path.join(os.tmpdir(), "crius-improver-worktrees-"));
  const improver = new ProjectImprover({ artifactRoot, worktreeRoot });
  const canonicalRepositoryPath = await realpath(repositoryPath);

  const result = await improver.initializeRun({
    repositoryPath,
    goal: "Improve repository reliability",
    implementationProvider: "codex",
    evaluatorProvider: "claude"
  }, new Date("2026-03-28T00:00:00Z"));

  const repositoryArtifact = await improver.artifactStore.readJson(result.contract.runId, "repository.json");
  const worktreeArtifact = await improver.artifactStore.readJson(result.contract.runId, "worktree.json");
  const providersArtifact = await improver.artifactStore.readJson(result.contract.runId, "providers.json");
  const journal = await readFile(path.join(result.runDir, "journal.jsonl"), "utf8");

  assert.equal(result.contract.currentStage, "planning");
  assert.equal(repositoryArtifact.rootPath, canonicalRepositoryPath);
  assert.notEqual(worktreeArtifact.worktreePath, canonicalRepositoryPath);
  assert.equal(git(worktreeArtifact.worktreePath, ["rev-parse", "--abbrev-ref", "HEAD"]), result.contract.branchName);
  assert.equal(providersArtifact.implementation.name, "codex");
  assert.match(journal, /run\.initialized/);
  assert.match(journal, /worktree\.created/);
});
