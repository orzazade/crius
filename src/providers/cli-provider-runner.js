import { writeFile, readFile } from "node:fs/promises";
import path from "node:path";
import { spawnCapture } from "../utils/process.js";

function buildMode(stage) {
  return stage === "implementation" ? "workspace-write" : "read-only";
}

function extractClaudeJson(stdout) {
  const parsed = JSON.parse(stdout);
  if (parsed.structured_output) return parsed.structured_output;
  if (typeof parsed.result === "string") return parsed.result;
  return parsed;
}

function extractGeminiJson(stdout) {
  const trimmed = stdout.trim();
  const parsed = JSON.parse(trimmed);
  if (parsed.result) return parsed.result;
  return parsed;
}

function parseJsonOnlyText(raw) {
  return JSON.parse(raw.trim());
}

export class CliProviderRunner {
  constructor(artifactStore) {
    this.artifactStore = artifactStore;
  }

  async run({ runId, stage, attempt, provider, cwd, prompt, schema }) {
    const timeoutSeconds = stage === "planning" ? 120 : stage === "implementation" ? 900 : 180;
    const runDir = await this.artifactStore.ensureRunDirectory(runId);
    const baseName = `${stage}-attempt-${attempt}`;
    const promptPath = path.join(runDir, `${baseName}.prompt.md`);
    const stdoutPath = path.join(runDir, `${baseName}.stdout.log`);
    const stderrPath = path.join(runDir, `${baseName}.stderr.log`);
    const responsePath = path.join(runDir, `${baseName}.response.txt`);
    const schemaPath = schema ? path.join(runDir, `${baseName}.schema.json`) : null;

    await writeFile(promptPath, prompt.endsWith("\n") ? prompt : `${prompt}\n`, "utf8");
    if (schemaPath) {
      await writeFile(schemaPath, `${JSON.stringify(schema, null, 2)}\n`, "utf8");
    }

    const invocation = this.buildInvocation({
      provider,
      cwd,
      stage,
      prompt,
      schema,
      schemaPath,
      responsePath
    });

    const startedAt = new Date();
    const { code, stdout, stderr, timedOut } = await spawnCapture(invocation.command, invocation.args, {
      cwd,
      input: invocation.stdin,
      timeoutMs: timeoutSeconds * 1000
    });
    const finishedAt = new Date();

    await Promise.all([
      writeFile(stdoutPath, stdout, "utf8"),
      writeFile(stderrPath, stderr, "utf8")
    ]);

    if (timedOut) {
      const error = new Error(`${provider} ${stage} timed out after ${timeoutSeconds}s`);
      error.stdout = stdout;
      error.stderr = stderr;
      error.command = invocation.command;
      error.args = invocation.args;
      error.timedOut = true;
      throw error;
    }

    if (code !== 0) {
      const error = new Error(`${provider} ${stage} failed with exit code ${code}`);
      error.stdout = stdout;
      error.stderr = stderr;
      error.command = invocation.command;
      error.args = invocation.args;
      throw error;
    }

    const response = await this.extractResponse({
      provider,
      stdout,
      responsePath,
      schema
    });

    if (typeof response === "string") {
      await writeFile(responsePath, response, "utf8");
    } else {
      await writeFile(responsePath, `${JSON.stringify(response, null, 2)}\n`, "utf8");
    }

    return {
      provider,
      command: invocation.command,
      args: invocation.args,
      cwd,
      promptPath,
      stdoutPath,
      stderrPath,
      responsePath,
      schemaPath,
      startedAt: startedAt.toISOString(),
      finishedAt: finishedAt.toISOString(),
      durationMs: finishedAt.getTime() - startedAt.getTime(),
      output: response
    };
  }

  buildInvocation({ provider, cwd, stage, prompt, schema, schemaPath, responsePath }) {
    const mode = buildMode(stage);

    if (provider === "codex") {
      const args = ["exec", "-", "--cd", cwd, "--color", "never", "--output-last-message", responsePath];
      if (schemaPath) args.push("--output-schema", schemaPath);
      if (mode === "workspace-write") {
        args.push("--full-auto");
      } else {
        args.push("--sandbox", "read-only");
      }

      return {
        command: "codex",
        args,
        stdin: prompt
      };
    }

    if (provider === "claude") {
      const args = ["-p", "--output-format", "json", "--no-session-persistence"];
      if (mode === "workspace-write") {
        args.push("--dangerously-skip-permissions");
      } else {
        args.push("--permission-mode", "plan");
      }
      if (schema) {
        args.push("--json-schema", JSON.stringify(schema));
      }

      return {
        command: "claude",
        args,
        stdin: prompt
      };
    }

    if (provider === "gemini") {
      const args = [
        "-p",
        prompt,
        "--output-format",
        "text"
      ];
      if (mode === "workspace-write") {
        args.push("--approval-mode", "yolo", "--sandbox");
      } else {
        args.push("--approval-mode", "plan");
      }

      return {
        command: "gemini",
        args,
        stdin: null
      };
    }

    throw new Error(`Unsupported provider: ${provider}`);
  }

  async extractResponse({ provider, stdout, responsePath, schema }) {
    if (provider === "codex") {
      const fileContent = await readFile(responsePath, "utf8");
      return schema ? parseJsonOnlyText(fileContent) : fileContent;
    }

    if (provider === "claude") {
      return extractClaudeJson(stdout);
    }

    if (provider === "gemini") {
      return schema ? extractGeminiJson(stdout) : stdout;
    }

    throw new Error(`Unsupported provider: ${provider}`);
  }
}
