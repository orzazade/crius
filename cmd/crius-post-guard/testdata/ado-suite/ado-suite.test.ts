// Tests for ado-suite.ts run's checks before a push, on throwaway local repos. No network: `az` is a stub on PATH that
// writes a marker and fails, so a run that passes every check stops at "no az token" (rc 2) before any push or ADO call.
// ADO_SUITE_SCRIPT names the script to test (default: the file beside this one).
// Run: bun test ado-suite.test.ts      (red proof: ADO_SUITE_SCRIPT=<old copy> bun test ado-suite.test.ts)
import { afterAll, beforeAll, expect, test } from 'bun:test';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdtempSync, realpathSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const SCRIPT = process.env.ADO_SUITE_SCRIPT ?? join(import.meta.dir, 'ado-suite.ts');
let root = '';
let stubs = '';
let azMarker = '';
const empty = () => join(root, 'empty-bunfig.toml');
// The credit texts are built from parts so no file holds them whole.
const coAuthored = ['Co', 'Authored-By: Cl'].join('-') + 'aude <noreply@example.com>';

// g runs git for the fixtures with no global or system config.
const g = (cwd: string, ...args: string[]) => {
  const r = spawnSync('git', ['-c', 'user.name=Test', '-c', 'user.email=t@example.com', '-c', 'protocol.file.allow=always', ...args],
    { cwd, encoding: 'utf8', env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: '/dev/null' } });
  if (r.status !== 0) throw new Error(`git ${args.join(' ')}: ${r.stderr}`);
  return r.stdout.trim();
};

// repo makes a worktree with one commit on origin/develop and one after it, on branch feature/x.
const repo = (name: string, msg = 'change'): string => {
  const wt = join(root, name);
  g(root, 'init', '-q', '-b', 'develop', wt);
  writeFileSync(join(wt, 'a.txt'), 'a\n');
  g(wt, 'add', 'a.txt');
  g(wt, 'commit', '-q', '-m', 'base');
  g(wt, 'update-ref', 'refs/remotes/origin/develop', 'HEAD');
  g(wt, 'checkout', '-q', '-b', 'feature/x');
  writeFileSync(join(wt, 'a.txt'), 'b\n');
  g(wt, 'commit', '-q', '-am', msg);
  return wt;
};

const suite = (args: string[], env: NodeJS.ProcessEnv = {}) => {
  rmSync(azMarker, { force: true });
  const r = spawnSync('bun', ['--no-env-file', `--config=${empty()}`, SCRIPT, ...args],
    { cwd: root, encoding: 'utf8', env: { ...process.env, PATH: `${stubs}:${process.env.PATH}`, ...env } });
  return { rc: r.status, err: r.stderr ?? '', azCalled: existsSync(azMarker) };
};
const run = (wt: string, env: NodeJS.ProcessEnv = {}) => suite(['run', wt, 'feature/x'], env);

beforeAll(() => {
  root = realpathSync(mkdtempSync(join(tmpdir(), 'ado-suite-test-')));
  stubs = join(root, 'stubs');
  spawnSync('mkdir', ['-p', stubs]);
  azMarker = join(root, 'az-called');
  writeFileSync(join(stubs, 'az'), `#!/bin/sh\ntouch '${azMarker}'\nexit 1\n`);
  chmodSync(join(stubs, 'az'), 0o755);
  writeFileSync(empty(), '');
});
afterAll(() => rmSync(root, { recursive: true, force: true }));

test('a clean worktree passes every check and stops only at the az stub (positive control)', () => {
  const r = run(repo('clean'));
  expect(r.err).toContain('no az token');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(true);
});

// The real svc_quote worktrees are linked ones (git worktree add) without extensions.worktreeConfig.
const linked = (name: string): string => {
  const main = repo(`${name}-main`);
  const wt = join(root, name);
  g(main, 'worktree', 'add', '-q', '-b', 'feature/y', wt);
  writeFileSync(join(wt, 'a.txt'), 'linked\n');
  g(wt, 'commit', '-q', '-am', 'linked change');
  return wt;
};

test('a clean linked worktree (the real shape) passes every check and stops only at the az stub', () => {
  const r = run(linked('linked-clean'));
  expect(r.err).toContain('no az token');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(true);
});

test("a linked worktree's own config.worktree key off the allowlist is refused", () => {
  const wt = linked('linked-wtcfg');
  g(wt, 'config', 'extensions.worktreeConfig', 'true');
  g(wt, 'config', '--worktree', 'core.sshCommand', 'touch /tmp/x');
  const r = run(wt);
  expect(r.err).toContain('has git config keys ado-suite does not allow (core.sshcommand)');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

test("a submodule's own core.fsmonitor does not run, and the gitlink is refused", () => {
  const sub = repo('subsrc');
  const wt = repo('withsub');
  g(wt, 'submodule', 'add', '-q', sub, 'sub');
  g(wt, 'commit', '-q', '-m', 'add sub');
  const ran = join(root, 'fsmonitor-ran');
  const hook = join(root, 'fsmon.sh');
  writeFileSync(hook, `#!/bin/sh\ntouch '${ran}'\nexit 1\n`);
  chmodSync(hook, 0o755);
  g(join(wt, 'sub'), 'config', 'core.fsmonitor', hook);
  // the submodule's file changed, so a status that looks inside it has to ask its fsmonitor
  writeFileSync(join(wt, 'sub', 'a.txt'), 'dirty\n');
  const r = run(wt);
  expect(existsSync(ran) ? 'fsmonitor ran' : 'fsmonitor not run').toBe('fsmonitor not run');
  expect(r.err).toContain('has a submodule (gitlink) at sub');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

for (const [key, value] of [['http.curloptResolve', 'dev.azure.com:443:127.0.0.1'], ['core.alternateRefsCommand', 'touch /tmp/x'],
  ['http.sslCAPath', '/tmp'], ['url.https://evil.example/.insteadOf', 'https://dev.azure.com/'], ['include.path', '/tmp/x.cfg']]) {
  test(`a local git config key off the allowlist is refused: ${key}`, () => {
    const wt = repo(`cfg-${key.replace(/[^a-z]/gi, '')}`);
    g(wt, 'config', key, value);
    const r = run(wt);
    expect(r.err).toContain('has git config keys ado-suite does not allow');
    expect(r.err.toLowerCase()).toContain(key.toLowerCase());
    expect(r.rc).toBe(2);
    expect(r.azCalled).toBe(false);
  });
}

for (const msg of [`change\n\n${coAuthored}`, 'change\n\nGenerated with a tool']) {
  test(`AI credit in a commit since origin/develop is refused: ${msg.split('\n')[2].slice(0, 14)}`, () => {
    const r = run(repo(`credit-${msg.length}`, msg));
    expect(r.err).toContain('AI credit in origin/develop..');
    expect(r.rc).toBe(2);
    expect(r.azCalled).toBe(false);
  });
}

// review 11-06 P1: a clean filter in local config runs when git status compares a touched file
test('a filter driver in local config does not run before the config check refuses it', () => {
  const wt = repo('filter');
  const ran = join(root, 'filter-ran');
  g(wt, 'config', 'filter.x.clean', `sh -c 'touch ${ran}; cat'`);
  writeFileSync(join(wt, '.git', 'info', 'attributes'), '* filter=x\n');
  writeFileSync(join(wt, 'a.txt'), 'b\n'); // same content, new stat data
  const r = run(wt);
  expect(existsSync(ran) ? 'filter ran' : 'filter not run').toBe('filter not run');
  expect(r.err).toContain('has git config keys ado-suite does not allow (filter.x.clean)');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

// review 11-06 P2: a tag or branch named origin/develop shadows the remote ref and empties the range
for (const how of [['tag', 'origin/develop', 'HEAD'], ['branch', 'origin/develop', 'HEAD']]) {
  test(`AI credit is refused when a ${how[0]} named origin/develop points at HEAD`, () => {
    const wt = repo(`shadow-${how[0]}`, `change\n\n${coAuthored}`);
    g(wt, ...how);
    const r = run(wt);
    expect(r.err).toContain('AI credit in origin/develop..');
    expect(r.rc).toBe(2);
    expect(r.azCalled).toBe(false);
  });
}

// review 11-06 P2: a replace ref hides the credited commit from git log, but the push sends the real one
test('AI credit is refused when a replace ref hides the credited commit', () => {
  const wt = repo('replace', `change\n\n${coAuthored}`);
  const credited = g(wt, 'rev-parse', 'HEAD');
  const clean = g(wt, 'commit-tree', '-p', 'origin/develop', '-m', 'clean', `${credited}^{tree}`);
  g(wt, 'replace', credited, clean);
  const r = run(wt);
  expect(r.err).toMatch(/AI credit in origin\/develop\.\.|replace refs or grafts/);
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

test('a grafts file is refused', () => {
  const wt = repo('grafts');
  writeFileSync(join(wt, '.git', 'info', 'grafts'), `${g(wt, 'rev-parse', 'HEAD')}\n`);
  const r = run(wt);
  expect(r.err).toContain('replace refs or grafts');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

// review 11-06 P3: the commit pushed holds a gitlink though the index no longer does
test('a gitlink in the pushed commit is refused even when the index dropped it', () => {
  const sub = repo('subsrc2');
  const wt = repo('withsub2');
  g(wt, 'submodule', 'add', '-q', sub, 'sub');
  g(wt, 'commit', '-q', '-m', 'add sub');
  g(wt, 'rm', '-q', '--cached', 'sub');
  g(wt, 'config', '--remove-section', 'submodule.sub'); // so only the gitlink is left to refuse
  const r = run(wt);
  expect(r.err).toMatch(/has a submodule \(gitlink\) at sub|uncommitted changes/);
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

test('AI credit in the author name is refused', () => {
  const wt = repo('credit-author');
  writeFileSync(join(wt, 'a.txt'), 'c\n');
  g(wt, '-c', 'user.name=Cl' + 'aude', 'commit', '-q', '-am', 'plain');
  const r = run(wt);
  expect(r.err).toContain('AI credit in origin/develop..');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

test('a folder inside a worktree that is not its top is refused', () => {
  const wt = repo('nested');
  spawnSync('mkdir', ['-p', join(wt, 'inner')]);
  const r = run(join(wt, 'inner'));
  expect(r.err).toContain('is not the top folder of its git worktree');
  expect(r.rc).toBe(2);
  expect(r.azCalled).toBe(false);
});

test("GIT_* env from the caller does not reach the checks' git", () => {
  const wt = repo('gitenv');
  const ran = join(root, 'env-fsmonitor-ran');
  const hook = join(root, 'env-fsmon.sh');
  writeFileSync(hook, `#!/bin/sh\ntouch '${ran}'\nexit 1\n`);
  chmodSync(hook, 0o755);
  // GIT_CONFIG_PARAMETERS would add config to every git call, here an fsmonitor
  const r = run(wt, { GIT_CONFIG_PARAMETERS: `'core.fsmonitor'='${hook}'`, GIT_CONFIG_COUNT: '1',
    GIT_CONFIG_KEY_0: 'core.fsmonitor', GIT_CONFIG_VALUE_0: hook });
  expect(existsSync(ran) ? 'fsmonitor ran' : 'fsmonitor not run').toBe('fsmonitor not run');
  expect(r.err).toContain('no az token');
  expect(r.rc).toBe(2);
});

test('usage and branch refusals keep their rc 2 and text', () => {
  const u = suite([]);
  expect(u.err).toContain('ado-suite: usage: bun ado-suite.ts run <worktree> [branch] | report <buildId>');
  expect(u.rc).toBe(2);
  const b = suite(['run', repo('mainbranch'), 'main']);
  expect(b.err).toContain('ado-suite: branch "main" refused: feature/* or wip/* only');
  expect(b.rc).toBe(2);
  expect(b.azCalled).toBe(false);
});
