import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, realpath, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { WorktreeManager } from "../src/improver/worktree-manager.js";

function git(cwd, args) {
  return execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8" }).trim();
}

async function createTempRepository() {
  const root = await mkdtemp(path.join(os.tmpdir(), "crius-repo-"));
  git(root, ["init", "-b", "main"]);
  git(root, ["config", "user.name", "Crius Test"]);
  git(root, ["config", "user.email", "crius@example.com"]);
  await writeFile(path.join(root, "README.md"), "# Temp Repo\n", "utf8");
  git(root, ["add", "README.md"]);
  git(root, ["commit", "-m", "init"]);
  return root;
}

test("WorktreeManager inspects repository state", async () => {
  const repositoryPath = await createTempRepository();
  const manager = new WorktreeManager();
  const snapshot = await manager.inspectRepository(repositoryPath);
  const canonicalRepositoryPath = await realpath(repositoryPath);

  assert.equal(snapshot.repositoryName, path.basename(repositoryPath));
  assert.equal(snapshot.rootPath, canonicalRepositoryPath);
  assert.equal(snapshot.branch, "main");
  assert.equal(snapshot.dirty, false);
  assert.match(snapshot.headSha, /^[0-9a-f]{40}$/);
});

test("WorktreeManager creates an isolated git worktree on a new branch", async () => {
  const repositoryPath = await createTempRepository();
  const worktreeRoot = await mkdtemp(path.join(os.tmpdir(), "crius-worktrees-"));
  const manager = new WorktreeManager({ worktreeRoot });
  const canonicalWorktreePath = await realpath(worktreeRoot);

  const created = await manager.createWorktree({
    repositoryPath,
    runId: "run-123",
    branchName: "codex/improve-test",
    baseRef: "main"
  });

  assert.equal(created.worktreePath, path.join(canonicalWorktreePath, "run-123"));
  assert.equal(git(created.worktreePath, ["rev-parse", "--abbrev-ref", "HEAD"]), "codex/improve-test");
  assert.equal(git(created.worktreePath, ["rev-parse", "--show-toplevel"]), created.worktreePath);
});
