import { appendFile } from "node:fs/promises";
import path from "node:path";

export class JournalStore {
  constructor(artifactStore) {
    this.artifactStore = artifactStore;
  }

  async append(runId, entry, now = new Date()) {
    const dir = await this.artifactStore.ensureRunDirectory(runId);
    const journalPath = path.join(dir, "journal.jsonl");
    const normalized = {
      timestamp: now.toISOString(),
      ...entry
    };

    await appendFile(journalPath, `${JSON.stringify(normalized)}\n`, "utf8");
    return journalPath;
  }
}

