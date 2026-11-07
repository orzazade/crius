// Full svc_quote suites in ADO pipeline 1119 (svc-quote), off the Mac (SPEEDUP-BRIEF step 1, lead 17:01).
//   bun ado-suite.ts run <worktree> [branch]   push the worktree's HEAD to <branch> over HTTPS (default: its current branch),
//                                              queue 1119 on that commit without the Images stage, wait, print the counts
//   bun ado-suite.ts report <buildId>          print the counts of a finished 1119 build (a PR build included)
// Branch: feature/* or wip/* only; never main or develop. The push is a plain push (never force): a rejected push stops (rc 3).
// The worktree must be clean, so the commit tested is the commit you name. Counts: one line per test project (from the
// published test runs), plus the web line from the Web job's vitest summary. rc 0 only when the build succeeded and every
// project ran more than 0 tests; rc 1 a failed build or 0 tests; rc 2 usage or setup; rc 3 push refused; rc 4 timed out.
// No secret is printed: the az token goes only into the git header (through GIT_CONFIG_* env, not argv) and the HTTP header.
// crius-post-guard lets `bun --no-env-file --config=<empty-bunfig.toml> <this file> report|run …` through without a click
// only while this file has the SHA-256 it pins, so any edit here asks again until the guard pins the new hash.
// Before a push, run refuses (rc 2) a worktree whose git could run code from it: every git call drops GIT_* env, reads no
// system or global config, and turns off fsmonitor, hooks, alternateRefsCommand, file:// transport and submodule
// recursion; a worktree with a submodule (gitlink) or any local git config key off the allowlist is refused, and so is a
// commit since origin/develop with AI credit in its message or names (tests: ado-suite.test.ts).
import { spawnSync } from 'node:child_process';
import { existsSync, realpathSync } from 'node:fs';

const ORG = 'https://dev.azure.com/Appxite';
const PROJ = 'AppXite%20Platform';
const REPO_URL = `${ORG}/AppXite%20Platform/_git/svc_quote`;
const PIPELINE = 1119;
const WAIT_MIN = Number(process.env.ADO_SUITE_WAIT_MIN ?? 45);

const die = (rc: number, msg: string): never => { console.error(`ado-suite: ${msg}`); process.exit(rc); };
// Refuse is a check that stops the run: main prints it as die does.
export class Refuse extends Error { constructor(public rc: number, msg: string) { super(msg); } }
const sh = (cmd: string, args: string[], cwd?: string, env?: NodeJS.ProcessEnv) => {
  const r = spawnSync(cmd, args, { cwd, env, encoding: 'utf8' });
  return { rc: r.status ?? 1, out: (r.stdout ?? '').trim(), err: (r.stderr ?? '').trim() };
};

// gitEnv is this process's env without any GIT_* variable, with no system or global git config and no prompt; extra
// adds GIT_CONFIG_COUNT/KEY/VALUE settings (the token header), which never reach argv.
export const gitEnv = (extra: Record<string, string> = {}): NodeJS.ProcessEnv => {
  const env: NodeJS.ProcessEnv = {};
  for (const [k, v] of Object.entries(process.env)) if (!k.startsWith('GIT_')) env[k] = v;
  const keys = Object.entries(extra);
  return {
    ...env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null', GIT_TERMINAL_PROMPT: '0',
    ...(keys.length ? { GIT_CONFIG_COUNT: String(keys.length) } : {}),
    ...Object.fromEntries(keys.flatMap(([k, v], i) => [[`GIT_CONFIG_KEY_${i}`, k], [`GIT_CONFIG_VALUE_${i}`, v]])),
  };
};
export const GIT_SAFE = ['-c', 'core.fsmonitor=', '-c', 'core.hooksPath=/dev/null', '-c', 'core.alternateRefsCommand=',
  '-c', 'protocol.file.allow=never', '-c', 'submodule.recurse=false'];
export const git = (wt: string, args: string[], extra: Record<string, string> = {}) =>
  sh('git', ['-C', wt, '--no-replace-objects', ...GIT_SAFE, ...args], undefined, gitEnv(extra));

const token = (): string => {
  const r = sh('az', ['account', 'get-access-token', '--resource', '499b84ac-1321-427f-aa17-267ca6975798', '--query', 'accessToken', '-o', 'tsv']);
  if (r.rc !== 0 || !r.out) die(2, 'no az token (az login?)');
  return r.out;
};
const api = async (t: string, path: string, init: RequestInit = {}) => {
  const res = await fetch(`${ORG}/${PROJ}/_apis/${path}`, {
    ...init, headers: { Authorization: `Bearer ${t}`, 'Content-Type': 'application/json', ...(init.headers ?? {}) },
  });
  const text = await res.text();
  if (!res.ok) die(2, `${init.method ?? 'GET'} ${path} -> ${res.status} ${text.slice(0, 300)}`);
  return text;
};

export const branchAllowed = (b: string) => /^(feature|wip)\/[A-Za-z0-9._\/-]+$/.test(b) && !b.includes('..');

// The local (and per-worktree) git config keys the svc_quote worktrees hold (all 83 under svc_quote/wt on 2026-11-06),
// plus extensions.worktreeconfig. Any other key may run a program or reroute the push (fsmonitor, hooksPath, url.insteadOf,
// http.curloptResolve, include.path, …), so it stops the run.
export const configAllowed = (k: string) =>
  /^(core\.(repositoryformatversion|filemode|bare|logallrefupdates|ignorecase|precomposeunicode)|extensions\.worktreeconfig|remote\.origin\.(url|fetch)|branch\..+\.(remote|merge|vscode-merge-base))$/.test(k);

export const creditRe = /claude|co-authored|anthropic|generated with/i;

// checkConfig throws Refuse when the worktree has a gitlink in its index, a local or per-worktree git config key off the
// allowlist, or replace refs or grafts (they make git log show other commits than the push sends). It runs before any
// git call that may start a configured program (status runs filter drivers).
export function checkConfig(wt: string): void {
  const files = git(wt, ['ls-files', '-s']);
  if (files.rc !== 0) throw new Refuse(2, `git ls-files failed in ${wt}`);
  const link = files.out.split('\n').find((l) => l.startsWith('160000 '));
  if (link) throw new Refuse(2, `${wt} has a submodule (gitlink) at ${link.split('\t')[1]}: refused, ado-suite pushes no submodules`);
  // git reads a worktree's own config.worktree only with extensions.worktreeConfig on (and --worktree fails without it
  // in a linked worktree, which every svc_quote worktree is)
  const perWorktree = git(wt, ['config', '--local', '--no-includes', '--type=bool', 'extensions.worktreeConfig']).out === 'true';
  for (const scope of perWorktree ? ['--local', '--worktree'] : ['--local']) {
    const c = git(wt, ['config', scope, '--no-includes', '--name-only', '--list']);
    if (c.rc !== 0) throw new Refuse(2, `git config ${scope} --list failed in ${wt}`);
    const bad = c.out.split('\n').filter((k) => k && !configAllowed(k));
    if (bad.length) throw new Refuse(2, `${wt} has git config keys ado-suite does not allow (${bad.join(', ')}): remove them first`);
  }
  const grafts = git(wt, ['rev-parse', '--path-format=absolute', '--git-path', 'info/grafts']).out;
  const replaced = git(wt, ['for-each-ref', '--count=1', '--format=%(refname)', 'refs/replace/']).out;
  if (!grafts || existsSync(grafts) || replaced) throw new Refuse(2, `${wt} has replace refs or grafts (${replaced || grafts}): refused`);
}

// checkWorktree runs every check before a push and gives the one sha to push and the branch; it throws Refuse otherwise.
export function checkWorktree(wt: string, branchArg?: string): { sha: string; branch: string } {
  if (git(wt, ['rev-parse', '--git-dir']).rc !== 0) throw new Refuse(2, `${wt} is not a git worktree`);
  checkConfig(wt);
  let real = '';
  try { real = realpathSync(wt); } catch { throw new Refuse(2, `${wt} is not a git worktree`); }
  const top = git(wt, ['rev-parse', '--show-toplevel']).out;
  if (top !== real) throw new Refuse(2, `${wt} is not the top folder of its git worktree (that is ${top || 'unknown'})`);
  const st = git(wt, ['status', '--porcelain', '--ignore-submodules=all', '--untracked-files=no']);
  if (st.rc !== 0) throw new Refuse(2, `git status failed in ${wt}: ${st.err.split('\n').slice(-1)[0]}`);
  if (st.out) throw new Refuse(2, `${wt} has uncommitted changes to tracked files; commit them first (the run tests HEAD)`);
  const sha = git(wt, ['rev-parse', '--verify', 'HEAD^{commit}']).out;
  if (!/^[0-9a-f]{40}$/.test(sha)) throw new Refuse(2, `${wt} has no HEAD commit`);
  const branch = branchArg ?? git(wt, ['symbolic-ref', '--short', 'HEAD']).out;
  if (!branchAllowed(branch)) throw new Refuse(2, `branch "${branch}" refused: feature/* or wip/* only`);
  const tree = git(wt, ['ls-tree', '-r', sha]);
  const inTree = tree.out.split('\n').find((l) => l.startsWith('160000 '));
  if (tree.rc !== 0 || inTree) throw new Refuse(2, `${wt} has a submodule (gitlink) at ${inTree?.split('\t')[1] ?? '?'} in ${sha.slice(0, 8)}: refused, ado-suite pushes no submodules`);
  // the full ref: a tag or branch named origin/develop would come first
  if (git(wt, ['rev-parse', '--verify', '-q', 'refs/remotes/origin/develop^{commit}']).rc !== 0)
    throw new Refuse(2, `${wt} has no origin/develop to check the commits against`);
  const log = git(wt, ['log', '--format=%H%n%an <%ae>%n%cn <%ce>%n%B', `refs/remotes/origin/develop..${sha}`]);
  if (log.rc !== 0) throw new Refuse(2, `git log origin/develop..${sha.slice(0, 8)} failed in ${wt}`);
  const hit = log.out.split('\n').find((l) => creditRe.test(l));
  if (hit) throw new Refuse(2, `AI credit in origin/develop..${sha.slice(0, 8)} ("${hit.trim().slice(0, 80)}"): refused, remove it first`);
  return { sha, branch };
}

// The web job prints vitest's summary, e.g. "      Tests  697 passed (697)" or "Tests  2 failed | 695 passed (697)".
export const webCount = (log: string): { passed: number; failed: number; total: number } | null => {
  const plain = log.replace(/\x1b\[[0-9;]*m/g, '');   // the agent log keeps vitest's colour codes
  const m = [...plain.matchAll(/Tests\s+(?:(\d+) failed \| )?(\d+) passed(?: \| \d+ \w+)* \((\d+)\)/g)].pop();
  return m ? { failed: Number(m[1] ?? 0), passed: Number(m[2]), total: Number(m[3]) } : null;
};

async function report(t: string, id: number): Promise<number> {
  const b = JSON.parse(await api(t, `build/builds/${id}?api-version=7.1`));
  console.log(`build ${id} ${b.sourceBranch} ${String(b.sourceVersion).slice(0, 8)} ${b.status} ${b.result ?? ''} ${b._links?.web?.href ?? ''}`);
  const runs = JSON.parse(await api(t, `test/runs?buildUri=vstfs:///Build/Build/${id}&includeRunDetails=true&api-version=7.1`)).value ?? [];
  let bad = 0;
  for (const r of runs) {
    const one = JSON.parse(await api(t, `test/Runs/${r.id}/results?$top=1&api-version=7.1`)).value?.[0];
    const name = String(one?.automatedTestStorage ?? r.name).replace(/\.dll$/, '');
    const failed = r.totalTests - r.passedTests - (r.notApplicableTests ?? 0);
    console.log(`  ${name}: ${r.passedTests}/${r.totalTests} passed, ${r.notApplicableTests ?? 0} skipped, ${failed} other`);
    if (r.totalTests === 0) bad++;
  }
  if (runs.length === 0) { console.log('  no published test runs'); bad++; }
  const tl = JSON.parse(await api(t, `build/builds/${id}/timeline?api-version=7.1`)).records ?? [];
  const task = tl.find((x: any) => x.type === 'Task' && x.name === 'npm run check');
  if (!task?.log?.id) { console.log('  web: no npm run check log'); bad++; }
  else {
    const w = webCount(await api(t, `build/builds/${id}/logs/${task.log.id}?api-version=7.1`, { headers: { Accept: 'text/plain' } }));
    if (!w) { console.log(`  web: no vitest summary in the log (task ${task.result})`); bad++; }
    else console.log(`  web: ${w.passed}/${w.total} passed, ${w.failed} failed (npm run check ${task.result})`);
  }
  if (b.status !== 'completed') return 4;
  return b.result === 'succeeded' && bad === 0 ? 0 : 1;
}

async function run(wt: string, branchArg?: string): Promise<number> {
  const { sha, branch } = checkWorktree(wt, branchArg);
  const t = token();
  const p = git(wt, ['push', '-q', REPO_URL, `${sha}:refs/heads/${branch}`], { 'http.extraheader': `AUTHORIZATION: bearer ${t}` });
  if (p.rc !== 0) die(3, `push of ${sha.slice(0, 8)} to ${branch} refused: ${p.err.split('\n').slice(-2).join(' ').replaceAll(t, '***')}`);
  console.log(`pushed ${sha.slice(0, 8)} to ${branch}`);
  const q = JSON.parse(await api(t, `pipelines/${PIPELINE}/runs?api-version=7.1`, {
    method: 'POST',
    body: JSON.stringify({ stagesToSkip: ['Images'], resources: { repositories: { self: { refName: `refs/heads/${branch}`, version: sha } } } }),
  }));
  console.log(`queued build ${q.id} ${q._links?.web?.href ?? ''}`);
  const end = Date.now() + WAIT_MIN * 60_000;
  for (;;) {
    const b = JSON.parse(await api(token(), `build/builds/${q.id}?api-version=7.1`));
    if (b.status === 'completed') break;
    if (Date.now() > end) { console.log(`not done after ${WAIT_MIN} min`); return report(token(), q.id); }
    await new Promise((r) => setTimeout(r, 60_000));
  }
  return report(token(), q.id);
}

if (import.meta.main) {
  const [cmd, a, b] = process.argv.slice(2);
  try {
    if (cmd === 'run' && a) process.exit(await run(a, b));
    else if (cmd === 'report' && /^\d+$/.test(a ?? '')) process.exit(await report(token(), Number(a)));
    else die(2, 'usage: bun ado-suite.ts run <worktree> [branch] | report <buildId>');
  } catch (e) {
    if (e instanceof Refuse) die(e.rc, e.message);
    throw e;
  }
}
