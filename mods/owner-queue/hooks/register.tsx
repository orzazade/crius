// The owner queue beside the Crius chat (DESIGN.md beside this folder). crius-queue holds the queue; this mod reads it
// every 5 s and draws it in four places: the pane, the status line, the band above the prompt and /waiting. An answer
// is recorded with crius-queue first, then submitted into the chat as "[queue qN] <answer>" so Crius acts on it.
import { atom, read, update } from 'claude-code'
import type { EngineInterface, Register } from 'claude-code'

import type { Item, View } from '../types'

const PANE = 'owner-queue'
const TITLE = 'Waiting on you'
const POLL_MS = 5000

const C = {
  pane: '#1b1a18',
  fg: '#e6e3da',
  soft: '#c9c5bb',
  dim: '#9a968c',
  box: '#3a3935',
  accent: '#e08a63',
  accentFg: '#f0a988',
  ok: '#8fc27e',
  badge: '#161615',
}

// Change the tag when View's fields change: it declines what an earlier version stored, which outlives a reload.
const view = atom({ plugin: 'owner-queue', key: 'view' } as const, null, { shape: 'view-1' })
const error = atom({ plugin: 'owner-queue', key: 'error' } as const, null)
const busy = atom({ plugin: 'owner-queue', key: 'busy' } as const, null)
const outbox = atom({ plugin: 'owner-queue', key: 'outbox' } as const, [])

// send submits an answer line into the Crius chat and says whether it went. One that did not (a hook or the engine
// dropped it, or the call failed) is a toast: the answer is recorded, so the owner types the line themselves.
async function send($: EngineInterface, line: string): Promise<boolean> {
  let why: string
  try {
    const r = await $.prompt.submit({ text: line, asUser: true })
    if (r.drop === undefined) {
      return true
    }
    why = r.drop
  } catch (e) {
    why = e instanceof Error ? e.message : String(e)
  }
  $.ui.toast(`Recorded, but not sent to Crius (${why}). Type it yourself: ${line}`)
  return false
}

// tick runs every second from session.start: it submits the answers /waiting recorded, and reads the queue every 5 s;
// while the mod is off it looks every 5 s whether this is Crius's tab now.
// A line leaves the outbox before it is sent, so a slow submit is never sent twice; one that fails says so (send).
let ticks = 0
let ticking = false
async function tick($: EngineInterface): Promise<void> {
  if (ticking) {
    return
  }
  ticking = true
  try {
    if (!active) {
      if (++ticks % (POLL_MS / 1000) === 0 && (await inCriusTab($))) {
        await turnOn($)
      }
      return
    }
    const lines = await read($, outbox)
    if (lines.length > 0) {
      await update($, outbox, list => list.slice(lines.length))
      for (const line of lines) {
        await send($, line)
      }
    }
    if (++ticks % (POLL_MS / 1000) === 0) {
      await refresh($)
    }
  } finally {
    ticking = false
  }
}

// The CLI from Crius's own bin folder, through sh so $HOME is the host's.
const cli = (...args: string[]) => ['/bin/sh', '-c', 'exec "$HOME/.crius/bin/crius-queue" "$@"', 'crius-queue', ...args]

const status = (v: View | null, err: string | null) =>
  err !== null
    ? `⚑ owner queue FAILED: ${err.slice(0, 120)}`
    : v === null
      ? '⚑ owner queue: reading…'
      : v.open.length === 0
        ? '✓ nothing waits on you'
        : `⚑ ${v.open.length} waiting on you`

async function refresh($: EngineInterface): Promise<void> {
  let next: View | null = null
  let err: string | null = null
  try {
    const r = await $.process.run(cli('list', '--json'), { timeoutMs: 10000 })
    if (r.exitCode !== 0) {
      err = (r.stderr || r.stdout || `exit ${r.exitCode}`).trim()
    } else {
      next = JSON.parse(r.stdout) as View
      if (!Array.isArray(next.open) || !Array.isArray(next.answered)) {
        throw new Error('crius-queue list --json printed no open/answered lists')
      }
    }
  } catch (e) {
    err = e instanceof Error ? e.message : String(e)
  }
  // a failed read keeps the last list on screen and says it failed: never an empty list that looks like "nothing waits"
  if (next !== null) {
    await update($, view, () => next)
  }
  await update($, error, () => err)
  $.ui.status(status(next ?? (await read($, view)), err))
}

// answer records text as row id's answer, then submits it to Crius. A refused answer (already answered, unknown row)
// is said and submits nothing: in the reply to /waiting (later), or as a toast for a press in the pane, which has no
// reply.
async function answer($: EngineInterface, id: string, text: string, later = false): Promise<string> {
  const say = (s: string) => {
    if (!later) {
      $.ui.toast(s)
    }
    return s
  }
  const t = text.trim()
  if (t === '') {
    return 'Type an answer first.'
  }
  if ((await read($, busy)) !== null) {
    return say('An answer is being sent; try again in a moment.')
  }
  await update($, busy, () => id)
  try {
    const r = await $.process.run(cli('answer', id, '--by', 'owner', t), { timeoutMs: 10000 })
    if (r.exitCode !== 0) {
      const why = (r.stderr || r.stdout).trim()
      return say(`Not sent: ${why}`)
    }
    const line = r.stdout.trim() // "[queue q2] <answer>", the id resolved from a number
    if (later) {
      // a command.run hook may not submit (the prompt would wait on the turn the hook holds), nor may a timer it
      // starts: the line waits in the outbox for the session's own tick
      await update($, outbox, list => [...list, line])
      return `Recorded; sending to Crius: ${line}`
    }
    return (await send($, line)) ? `Sent to Crius: ${line}` : `Recorded, but not sent to Crius. Type it yourself: ${line}`
  } finally {
    await update($, busy, () => null)
    await refresh($)
  }
}

function listText(v: View | null, err: string | null): string {
  const out: string[] = []
  if (err !== null) {
    out.push(`The owner queue could not be read: ${err}`)
  }
  if (v === null) {
    return out.join('\n') || 'The owner queue is not read yet.'
  }
  if (v.open.length === 0) {
    out.push('✓ Nothing waits on you.')
  } else {
    out.push(`Waiting on you (${v.open.length}), oldest first`)
    v.open.forEach((r, i) => {
      out.push(`${i + 1} ${r.title} · ${r.from} · ${r.age}`)
      for (const [k, val] of [['Do', r.do], ['Why you', r.why], ['Unblocks', r.unblocks]] as const) {
        if (val && val !== r.title) {
          out.push(`    ${k.padEnd(9)} ${val}`)
        }
      }
      if (r.options?.length) {
        out.push(`    ${'Options'.padEnd(9)} ${r.options.join(' | ')}`)
      }
    })
    out.push('Answer by number, any text: "/waiting 2 yes", "/waiting 1 not before Monday".')
  }
  if (v.answered.length > 0) {
    out.push('Answered today')
    for (const r of v.answered) {
      out.push(`  ✓ ${r.title} · ${r.answer ?? ''}`)
    }
  }
  return out.join('\n')
}

// The mod is installed for Crius's folder, where other sessions run too: it turns on in Crius's tab alone, the cmux
// surface Crius's role file names (crius-hook keeps ~/.crius/roles/crius.lead.json). A role file it cannot read is said
// in the status line, never a quiet "off".
let active = false
// the row ids in the order /waiting printed them last: what "/waiting N" means
let printed: string[] | null = null

async function turnOn($: EngineInterface): Promise<void> {
  active = true
  await $.command.register({
    name: 'waiting',
    description: 'What waits on you; "/waiting 2 yes" answers item 2',
  })
  await refresh($)
  void $.ui.open({ id: PANE, title: TITLE, backgroundColor: C.pane })
}
async function inCriusTab($: EngineInterface): Promise<boolean> {
  const tab = await $.env.get('CMUX_SURFACE_ID')
  const home = await $.env.get('HOME')
  if (!tab || !home) {
    return false
  }
  try {
    const role = JSON.parse(await $.fs.read(`${home}/.crius/roles/crius.lead.json`)) as { surface?: string }
    return role.surface === tab
  } catch (e) {
    $.ui.status(`⚑ owner queue off: Crius's role file unreadable (${e instanceof Error ? e.message : String(e)})`)
    return false
  }
}

export const register: Register = on => {
  on('session.start', async ($, e, next) => {
    if (await inCriusTab($)) {
      await turnOn($)
    }
    // off, the tick looks again every 5 s: crius-hook writes Crius's tab into the role file in its own start hook,
    // which may run after this one
    $.clock.every(1000, () => void tick($).catch(e => $.ui.toast(`owner queue: ${e instanceof Error ? e.message : String(e)}`)))

    return next(e)
  })

  on('command.run', { command: 'waiting' }, async ($, e) => {
    const args = e.args.trim()
    if (args === '') {
      await refresh($)
      await $.ui.open({ id: PANE, title: TITLE, backgroundColor: C.pane })
      const v = await read($, view)
      printed = v?.open.map(r => r.id) ?? null
      return { text: listText(v, await read($, error)) }
    }
    const m = /^(\d+|q\d+)\s+([\s\S]+)$/.exec(args)
    if (m === null) {
      return { text: 'Answer as "/waiting <number> <text>", for example "/waiting 2 yes".' }
    }
    const [, n = '', text = ''] = m
    // a number is the place in the list /waiting printed last (the list refreshes every 5 s), sent as that row's id: a
    // row that closed or came in since cannot move another under the number
    if (/^\d+$/.test(n) && printed === null) {
      return { text: 'Type /waiting first: the numbers are those of the list it prints.' }
    }
    const id = /^\d+$/.test(n) ? printed?.[Number(n) - 1] : n
    if (id === undefined) {
      return { text: `No item ${n} in the list; /waiting shows it.` }
    }
    return { text: await answer($, id, text, true) }
  })

  on('ui.render', { component: 'AbovePrompt' }, async ($, e, next) => {
    const v = await read($, view)
    const err = await read($, error)
    if (!active || e.props.hasSurvey || (err === null && (v === null || v.open.length === 0))) {
      return next(e)
    }
    const { Box, Text } = $.ui.resolve(e)
    const oldest = v?.open[0]

    return (
      <Box borderStyle="round" borderColor={C.accent} paddingX={1} flexDirection="row" gap={2}>
        {err !== null ? (
          <Text color={C.accentFg} bold wrap="truncate-end">
            ⚑ owner queue FAILED: {err}
          </Text>
        ) : (
          <Text color={C.accentFg} bold>
            ⚑ {v?.open.length} waiting on you
          </Text>
        )}
        {oldest && (
          <Text color={C.soft} wrap="truncate-end">
            oldest: {oldest.title} ({oldest.age})
          </Text>
        )}
        <Box flexGrow={1} />
        <Text color={C.dim}>/waiting to see all</Text>
      </Box>
    )
  })

  on('ui.render', { component: 'Pane', requestId: PANE }, async ($, e) => {
    const ui = $.ui.resolve(e)
    const { Box, Text, Button } = ui
    const Input = 'Input' in ui ? ui.Input : undefined
    const v = await read($, view)
    const err = await read($, error)
    const sending = await read($, busy)
    const open = v?.open ?? []

    const part = (label: string, value: string | undefined, color: string) =>
      value ? (
        <Box flexDirection="row">
          <Box width={10} flexShrink={0}>
            <Text color={C.dim}>{label}</Text>
          </Box>
          <Box flexShrink={1}>
            <Text color={color}>{value}</Text>
          </Box>
        </Box>
      ) : null

    const card = (r: Item, n: number) => (
      <Box borderStyle="round" borderColor={C.box} paddingX={1} flexDirection="column">
        <Box flexDirection="row" gap={1}>
          <Text color={C.accentFg} bold>
            {String(n)}
          </Text>
          <Box flexGrow={1} flexShrink={1}>
            <Text color={C.fg} bold wrap="truncate-end">
              {r.title}
            </Text>
          </Box>
          <Text color={C.dim}>
            {r.from} · {r.age}
          </Text>
        </Box>
        {part('Do', r.do && r.do !== r.title ? r.do : undefined, C.fg)}
        {part('Why you', r.why, C.soft)}
        {part('Unblocks', r.unblocks, C.soft)}
        {(r.options?.length ?? 0) > 0 && (
          <Box flexDirection="row" flexWrap="wrap" gap={1}>
            {(r.options ?? []).map((o, i) => (
              <Button
                key={`${r.id}:${i}`}
                label={o}
                variant={i === 0 ? 'primary' : undefined}
                onPress={() => void answer($, r.id, o)}
              />
            ))}
          </Box>
        )}
        {Input && (
          <Input
            key={`${r.id}:own`}
            placeholder="Your own answer…"
            submitLabel="send"
            onSubmit={text => void answer($, r.id, text)}
          />
        )}
        {sending === r.id && <Text color={C.dim}>sending…</Text>}
      </Box>
    )

    return (
      <Box flexDirection="column" gap={1}>
        <Box flexDirection="row" gap={1}>
          <Text color={C.fg} bold>
            {TITLE}
          </Text>
          <Text color={C.badge} backgroundColor={C.accent} bold>
            {` ${open.length} `}
          </Text>
          <Box flexGrow={1} />
          <Text color={C.dim}>oldest first</Text>
        </Box>
        {err !== null && (
          <Text color={C.accentFg} bold>
            ⚑ The queue could not be read, so this list may be old: {err}
          </Text>
        )}
        {v === null && err === null && <Text color={C.dim}>Reading the queue…</Text>}
        {v !== null && open.length === 0 && (
          <Box borderStyle="round" borderColor={C.box} paddingX={1} flexDirection="column">
            <Text color={C.ok} bold>
              ✓ Nothing waits on you.
            </Text>
            <Text color={C.dim}>Crius adds a row here when a lead needs your call.</Text>
          </Box>
        )}
        {open.map((r, i) => card(r, i + 1))}
        {(v?.answered.length ?? 0) > 0 && (
          <Box flexDirection="column">
            <Text color={C.dim}>Answered today</Text>
            {(v?.answered ?? []).map(r => (
              <Text color={C.dim} wrap="truncate-end">
                <Text color={C.ok}>✓ </Text>
                {r.title} · {r.answer ?? ''}
              </Text>
            ))}
          </Box>
        )}
      </Box>
    )
  })
}
