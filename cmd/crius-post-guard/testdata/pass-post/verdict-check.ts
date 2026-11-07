// verdict-check.ts --pr N --verdict FILE [--src FILE ...]
// Refuses (rc 6) a PASS verdict that lacks one line per ruling the PR text, the READY/NEED-QA lines of the PR, or the named
// files cite. Ruling = B<n> (each numbered point of its BUILD-DECISIONS section, B<n>.<k>) or B<n>.<k> alone.
// Verdict line: `B104.1: met — evidence` | `B104.1: not met — evidence` | `B104.1: n/a — reason`  (text after the dash is required).
import { readFileSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
const a = process.argv.slice(2); const get = (k: string) => { const i = a.indexOf(k); return i < 0 ? null : a[i + 1]; };
const pr = get('--pr'), vf = get('--verdict'); const srcs = a.flatMap((x, i) => x === '--src' ? [a[i + 1]] : []);
if (!pr || !vf) { console.error('usage: --pr N --verdict FILE [--src FILE ...]'); process.exit(2); }
const B = `${process.env.HOME}/.crius/state/quotes/build`;
const log = readFileSync(`${B}/BUILD-LOG.md`, 'utf8').split('\n').filter((l) => / (NEED-QA|READY) /.test(l) && new RegExp(`(PR |#)${pr}\\b|\\b${pr} `).test(l));
const prText = spawnSync('az', ['repos', 'pr', 'show', '--id', pr, '--org', 'https://dev.azure.com/Appxite', '--query', 'description', '-o', 'tsv'], { encoding: 'utf8' }).stdout ?? '';
const text = [prText, ...log, ...srcs.map((f) => readFileSync(f, 'utf8'))].join('\n');
const dec = readFileSync(`${B}/BUILD-DECISIONS.md`, 'utf8');
const section = (n: string) => { const m = dec.match(new RegExp(`^## B${n} [^\\n]*\\n([\\s\\S]*?)(?=^## B\\d|$(?![\\s\\S]))`, 'm')); return m ? m[1] : null; };
const need = new Set<string>();
for (const m of text.matchAll(/\bB(\d+)(?:\.(\d+))?\b/g)) {
  const [, n, k] = m; if (k) { need.add(`B${n}.${k}`); continue; }
  const s = section(n); if (s === null) continue;   // B-number with no decision section (e.g. a finding id)
  const pts = [...s.matchAll(/^(\d+)\. /gm)].map((x) => x[1]); if (pts.length === 0) need.add(`B${n}`); else for (const p of pts) need.add(`B${n}.${p}`);
}
// B<n> cited with parts: the bare form is covered by its parts
const verdict = readFileSync(vf, 'utf8');
const have = new Set([...verdict.matchAll(/^\s*[-*]?\s*(B\d+(?:\.\d+)?):\s*(met|not met|n\/a)\s*[—-]\s*\S.*$/gim)].map((x) => x[1]));
const missing = [...need].filter((r) => !have.has(r)).sort();
console.log(`rulings cited (${need.size}): ${[...need].sort().join(' ') || 'none'}`);
// Crius 11-06 11:26 (7833 x3, 7831 x2, 7840 x2 retries): name every fault in one run. A ruling whose line exists but is
// malformed is listed with that line and what is wrong, so one fix pass covers all of them.
const faults: string[] = [];
for (const r of missing) {
  const near = verdict.split('\n').filter((l) => new RegExp(`(^|[^\\d.])${r.replace('.', '\\.')}(?![\\d.])`).test(l));
  if (near.length === 0) {
    const base = r.split('.')[0];
    const bare = r.includes('.') && verdict.split('\n').some((l) => new RegExp(`\\b${base}(?![\\d.])`).test(l));
    faults.push(`${r}: no line${bare ? ` (a bare "${base}" line does not count: ${base} has numbered points, give each its own line)` : ''}`); continue;
  }
  for (const l of near) {
    const why = !/:\s*/.test(l.slice(l.indexOf(r) + r.length, l.indexOf(r) + r.length + 2)) ? `"${r}" must be followed by ":"`
      : !/:\s*(met|not met|n\/a)\b/i.test(l) ? 'status must be met, not met or n/a'
      : !/(met|n\/a)\s*[—-]\s*\S/i.test(l) ? 'needs " — <evidence>" after the status' : 'line must start with the ruling id';
    faults.push(`${r}: ${why}: ${l.trim().slice(0, 120)}`);
  }
}
// Crius 11-06 13:33 (old line 1 on 7852, 7833, 7827): line 1 is "# QA verdict: PR <id> (<what>) @ <head8> — PASS";
// with --head (pass-post.sh passes the PR head) the sha must be that head.
const head = get('--head'); const l1 = verdict.split('\n')[0];
const m1 = l1.match(new RegExp(`^# QA verdict: PR ${pr} \\(.+\\) @ ([0-9a-f]{8}) — PASS$`));
if (!m1) faults.push(`line 1 must read "# QA verdict: PR ${pr} (<what>) @ <head8> — PASS", it reads: ${l1.slice(0, 120)}`);
else if (head && m1[1] !== head.slice(0, 8)) faults.push(`line 1 names ${m1[1]}, the PR head is ${head.slice(0, 8)}`);
if (faults.length) { console.log(`REFUSED rc 6: ${faults.length} fault(s), each needs a "Bn.k: met|not met|n/a — evidence" line:`); for (const f of faults) console.log(`  - ${f}`); process.exit(6); }
console.log('verdict-check ok');
