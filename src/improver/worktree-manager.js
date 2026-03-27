import { mkdir, realpath } from "node:fs/promises";
import path from "node:path";
import { execFileAsync } from "../utils/exec.js";

function parsePorcelainStatus(stdout) {
  const lines = stdout
    .split("\n")
    .map((line) => line.trimEnd())
    .filter(Boolean);

  return {
    dirty: lines.length > 0,
    entries: lines
  };
}

export class WorktreeManager {
  constructor({ worktreeRoot = path.join(process.cwd(), ".crius", "worktrees") } = {}) {
    this.worktreeRoot = worktreeRoot;
  }

  getRunWorktreePath(runId) {
    return path.join(this.worktreeRoot, runId);
  }

  async inspectRepository(repositoryPath) {
    const rootResult = await execFileAsync("git", ["-C", repositoryPath, "rev-parse", "--show-toplevel"]);
    const rootPath = await realpath(rootResult.stdout.trim());

    const [headResult, branchResult, statusResult, originResult] = await Promise.all([
      execFileAsync("git", ["-C", rootPath, "rev-parse", "HEAD"]),
      execFileAsync("git", ["-C", rootPath, "rev-parse", "--abbrev-ref", "HEAD"]),
      execFileAsync("git", ["-C", rootPath, "status", "--short"]),
      execFileAsync("git", ["-C", rootPath, "remote", "get-url", "origin"]).catch(() => ({ stdout: "" }))
    ]);

    const status = parsePorcelainStatus(statusResult.stdout);

    return {
      rootPath,
      repositoryName: path.basename(rootPath),
      headSha: headResult.stdout.trim(),
      branch: branchResult.stdout.trim(),
      originUrl: originResult.stdout.trim() || null,
      dirty: status.dirty,
      statusEntries: status.entries
    };
  }

  async createWorktree({ repositoryPath, runId, branchName, baseRef }) {
    const worktreePath = this.getRunWorktreePath(runId);
    await mkdir(this.worktreeRoot, { recursive: true });

    await execFileAsync("git", [
      "-C",
      repositoryPath,
      "worktree",
      "add",
      "-b",
      branchName,
      worktreePath,
      baseRef
    ]);

    return {
      worktreePath: await realpath(worktreePath),
      branchName,
      baseRef
    };
  }
}
