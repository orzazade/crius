import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";

export class ArtifactStore {
  constructor(rootDir) {
    this.rootDir = rootDir;
  }

  runDir(runId) {
    return path.join(this.rootDir, runId);
  }

  async ensureRunDirectory(runId) {
    const dir = this.runDir(runId);
    await mkdir(dir, { recursive: true });
    return dir;
  }

  async writeJson(runId, fileName, value) {
    const dir = await this.ensureRunDirectory(runId);
    const filePath = path.join(dir, fileName);
    await writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`, "utf8");
    return filePath;
  }

  async readJson(runId, fileName) {
    const raw = await readFile(path.join(this.runDir(runId), fileName), "utf8");
    return JSON.parse(raw);
  }

  async writeText(runId, fileName, value) {
    const dir = await this.ensureRunDirectory(runId);
    const filePath = path.join(dir, fileName);
    await writeFile(filePath, value.endsWith("\n") ? value : `${value}\n`, "utf8");
    return filePath;
  }

  async readText(runId, fileName) {
    return readFile(path.join(this.runDir(runId), fileName), "utf8");
  }

  async initializeRun(contract, specMarkdown) {
    const state = {
      runId: contract.runId,
      currentStage: contract.currentStage,
      status: contract.status,
      revisionCount: contract.revisionCount,
      attempts: {
        planning: 0,
        implementation: 0,
        evaluation: 0,
        handoff: 0
      },
      lastStageResult: null,
      workspacePath: contract.workspacePath,
      worktreePath: contract.worktreePath,
      updatedAt: contract.updatedAt
    };

    await Promise.all([
      this.writeJson(contract.runId, "contract.json", contract),
      this.writeJson(contract.runId, "state.json", state),
      this.writeText(contract.runId, "spec.md", specMarkdown)
    ]);

    return this.runDir(contract.runId);
  }

  async updateContract(contract) {
    return this.writeJson(contract.runId, "contract.json", contract);
  }

  async updateState(runId, state) {
    return this.writeJson(runId, "state.json", state);
  }
}
