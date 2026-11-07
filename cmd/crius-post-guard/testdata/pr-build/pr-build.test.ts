// pr-build.sh with a stubbed az: it queues only when the PR head's azure-pipelines.yml is develop's, integration's, or
// the version where the branch left them (review 11-06 P2-3), and every lookup failure exits 2.
import { expect, test } from 'bun:test';
import { chmodSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const SCRIPT = process.env.PR_BUILD ?? join(import.meta.dir, 'pr-build.sh');
const sha = (c: string) => c.repeat(40);
const MC = sha('a'), SRC = sha('b'), DEV = sha('d'), INT = sha('e'), BASE_D = sha('1'), BASE_I = sha('2');
const TEAM = sha('7'), OLD = sha('8'), EDITED = sha('9');

// The stub answers each az call the script makes from the fixture env; an unset value fails like az does (rc 1).
const AZ = `#!/bin/bash
a="$*"; echo "$a" >> "$AZ_LOG"
val() { [ -n "\${!1+x}" ] || { echo "ERROR: stub has no $1" >&2; exit 1; }; echo "\${!1}"; }
case "$a" in
  *"pipelines runs list"*) val RUNS ;;
  *"pipelines run "*) echo 4242 ;;
  *"pr policy list"*) val EVAL ;;
  *"pr policy queue"*) ;;
  *"lastMergeCommit.commitId"*) val MC ;;
  *"lastMergeSourceCommit.commitId"*) val SRC ;;
  *"targetRefName"*) val TGT ;;
  *"ref list"*"heads/develop"*) val TIP_DEV ;;
  *"ref list"*"heads/integration/96365-b1"*) val TIP_INT ;;
  *"--resource commits"*) val P1 ;;
  *"--resource items"*) v=$(sed -E 's/.*versionDescriptor.version=([0-9a-f]{40}).*/\\1/' <<<"$a"); val "Y_$v" ;;
  *"--resource mergeBases"*) o=$(sed -E 's/.*otherCommitId=([0-9a-f]{40}).*/\\1/' <<<"$a"); val "B_$o" ;;
  *) echo "ERROR: stub does not know: $a" >&2; exit 1 ;;
esac
`;

function run(env: Record<string, string | undefined>) {
  const dir = mkdtempSync(join(tmpdir(), 'pr-build-'));
  writeFileSync(join(dir, 'az'), AZ);
  chmodSync(join(dir, 'az'), 0o755);
  const log = join(dir, 'az.log');
  writeFileSync(log, '');
  const base: Record<string, string> = {
    RUNS: '', EVAL: '', MC, SRC, TGT: 'refs/heads/integration/96365-b1', TIP_DEV: DEV, TIP_INT: INT, P1: INT,
    [`Y_${DEV}`]: TEAM, [`Y_${INT}`]: TEAM, [`B_${DEV}`]: BASE_D, [`B_${INT}`]: BASE_I,
    [`Y_${BASE_D}`]: OLD, [`Y_${BASE_I}`]: OLD, [`Y_${SRC}`]: TEAM,
  };
  const e: Record<string, string> = { PATH: `${dir}:/usr/bin:/bin`, AZ_LOG: log };
  for (const [k, v] of Object.entries({ ...base, ...env })) if (v !== undefined) e[k] = v;
  const p = Bun.spawnSync(['bash', SCRIPT, '7848'], { env: e });
  const calls = readFileSync(log, 'utf8');
  return { rc: p.exitCode, out: p.stdout.toString() + p.stderr.toString(), queued: /pipelines run |pr policy queue/.test(calls) };
}

test('the head keeps the integration or develop yml: queued', () => {
  for (const tips of [{}, { [`Y_${INT}`]: OLD }]) {
    const r = run(tips);
    expect(r.out).toContain('queued 4242');
    expect([r.rc, r.queued]).toEqual([0, true]);
  }
});

test('the head keeps the yml where the branch left develop (develop changed it since): queued', () => {
  const r = run({ [`Y_${DEV}`]: sha('5'), [`Y_${INT}`]: sha('6'), [`Y_${SRC}`]: OLD });
  expect([r.rc, r.queued]).toEqual([0, true]);
});

test('the head edits azure-pipelines.yml: refused with exit 2, nothing queued', () => {
  const r = run({ [`Y_${SRC}`]: EDITED });
  expect(r.out).toContain('is not develop');
  expect([r.rc, r.queued]).toEqual([2, false]);
});

test('the head has no azure-pipelines.yml: exit 2, nothing queued', () => {
  const r = run({ [`Y_${SRC}`]: undefined });
  expect(r.out).toContain('LOUD pr-build: cannot read azure-pipelines.yml');
  expect([r.rc, r.queued]).toEqual([2, false]);
});

test('every lookup failure exits 2 and queues nothing', () => {
  for (const [name, env] of Object.entries({
    'head read': { SRC: undefined },
    'head not a sha': { SRC: 'None' },
    'develop tip read': { TIP_DEV: undefined },
    'develop tip yml': { [`Y_${DEV}`]: undefined, [`Y_${SRC}`]: EDITED },
    'merge base read': { [`B_${DEV}`]: undefined, [`Y_${SRC}`]: EDITED },
    'merge base yml': { [`Y_${BASE_D}`]: undefined, [`Y_${SRC}`]: EDITED },
  })) {
    const r = run(env);
    expect([name, r.rc, r.queued]).toEqual([name, 2, false]);
    expect(r.out).toContain('LOUD pr-build');
  }
});

test('a stale merge on a policy target re-queues the policy only when the yml is the team\'s', () => {
  const ok = run({ P1: sha('3'), EVAL: 'ev-1' });
  expect([ok.rc, ok.queued]).toEqual([0, true]);
  const bad = run({ P1: sha('3'), EVAL: 'ev-1', [`Y_${SRC}`]: EDITED });
  expect([bad.rc, bad.queued]).toEqual([2, false]);
});
