import { expect, test } from 'claude-code/testing'

const PANE = {
  plugin: 'owner-queue',
  component: 'Pane',
  requestId: 'owner-queue',
  props: {
    title: 'Waiting on you',
    isFocused: false,
    bodyColumns: 60,
    placement: 'dock',
    scroll: { offset: 0, bodyRows: 40 },
    view: {},
  },
} as const

const BAND = {
  plugin: 'owner-queue',
  component: 'AbovePrompt',
  props: { hasSurvey: false, isWorking: false, maxRows: 10, bodyColumns: 80, scroll: { offset: 0, bodyRows: 10 } },
} as const

const row = (id: string, title: string, age: string, extra = {}) => ({
  id, title, from: 'Crius', opened: '2026-10-30T13:00', state: 'open', age, ...extra,
})

const TWO = {
  open: [
    row('q3', 'Restart Chrome', '4h', { from: 'Mac', do: 'Quit Chrome fully, then open it again.', why: 'Crius never drives your Chrome.', unblocks: 'Browser checks', options: ['Done', 'Remind me at 18:00'] }),
    row('q2', 'Start a build lead for the queue', '3m', { options: ['Yes, start it', 'Not now'] }),
  ],
  answered: [{ ...row('q1', 'Park ado-73525', '1h'), state: 'answered', answer: 'done', answeredAt: '2026-10-30T12:00' }],
  now: '2026-10-30T14:00',
}
const EMPTY = { open: [], answered: [], now: '2026-10-30T14:00' }

const ran = (stdout: string, exitCode = 0, stderr = '') => ({
  value: { exitCode, stdout, stderr, isStdoutTruncated: false, isStderrTruncated: false },
})

// The engine beneath the mod: crius-queue answers from `list`, and every call is kept.
function engine(on: Parameters<Parameters<typeof test>[1]>[1], list: () => object | 'fail', tab = 'S-CRIUS', drop = '') {
  const calls: string[][] = []
  const submitted: string[] = []
  const status: (string | undefined)[] = []
  const toasts: string[] = []
  const opened: string[] = []
  const commands: string[] = []
  // this tab, and Crius's role file naming Crius's tab
  on('env.get', (_, e) => ({ value: e.name === 'CMUX_SURFACE_ID' ? tab : e.name === 'HOME' ? '/h' : undefined }))
  on('fs.read', (_, e) =>
    e.path === '/h/.crius/roles/crius.lead.json' ? { value: JSON.stringify({ name: 'crius · lead', surface: 'S-CRIUS' }) } : { value: '' },
  )
  on('process.run', (_, e) => {
    const args = e.argv.slice(4)
    calls.push(args)
    if (args[0] === 'list') {
      const l = list()
      return l === 'fail' ? ran('', 1, 'crius-queue: the owner queue was never set up') : ran(JSON.stringify(l))
    }
    if (args[0] === 'answer' && args[1] === 'q9') {
      return ran('', 1, 'crius-queue: q9 is already answered')
    }
    return ran(`[queue ${args[1]}] ${args.slice(4).join(' ')}\n`)
  })
  on('prompt.submit', (_, e) => {
    if (drop !== '') {
      return { drop }
    }
    submitted.push(e.text)
    return { text: e.text }
  })
  on('ui.status', (_, e) => {
    status.push(e.text)
    return { value: undefined }
  })
  on('ui.toast', (_, e) => {
    toasts.push(e.text)
    return { value: undefined }
  })
  on('command.register', (_, e) => {
    commands.push(e.name)
    return { value: { command: e.name } }
  })
  on('ui.open', (_, e) => {
    opened.push(e.id)
    return { value: { isPlaced: true } }
  })
  // the session's tick runs only when the test fires it
  const ticks: (() => void)[] = []
  on('clock.every', () => new Promise(r => ticks.push(() => r({ value: undefined }))))
  on('session.start', (_, e) => ({ cwd: e.cwd }))
  // the engine's own band, which the mod leaves when nothing waits
  on('ui.render', ($, e) => {
    const { Box } = $.ui.resolve(e)
    return <Box />
  })
  const tick = async () => {
    ticks.shift()?.()
    await new Promise(r => setTimeout(r, 50))
  }
  return { calls, submitted, status, toasts, opened, commands, tick }
}

test('the pane lists what waits, oldest first, with count, parts, buttons and an own-answer field', async ($, on) => {
  const seen = engine(on, () => TWO)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(seen.status.at(-1)).toBe('⚑ 2 waiting on you')

  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /^ 2 $/ }))?.text).toBe(' 2 ')
  const titles = (await ui.findAll({ type: 'Text' })).map(t => t.text)
  expect(titles.indexOf('Restart Chrome') < titles.indexOf('Start a build lead for the queue')).toBe(true)
  expect(titles).toContain('Crius never drives your Chrome.')
  expect(titles).toContain('Answered today')
  expect(await ui.findAll({ type: 'Button' })).toHaveLength(4)
  expect(await ui.find({ type: 'Input', key: 'q3:own' })).toBeDefined()
  expect(await ui.find({ type: 'Input', key: 'q2:own' })).toBeDefined()
  await ui.unmount()
})

test('a button records the answer, then submits "[queue qN] <answer>" to Crius', async ($, on) => {
  const seen = engine(on, () => TWO)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.press({ key: 'q2:0' })
  await new Promise(r => setTimeout(r, 50))
  expect(seen.calls).toContainEqual(['answer', 'q2', '--by', 'owner', 'Yes, start it'])
  expect(seen.submitted).toEqual(['[queue q2] Yes, start it'])
  await ui.unmount()
})

test('the own-answer field sends its text the same way', async ($, on) => {
  const seen = engine(on, () => TWO)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.input({ key: 'q3:own', text: 'after lunch' })
  await new Promise(r => setTimeout(r, 50))
  expect(seen.calls).toContainEqual(['answer', 'q3', '--by', 'owner', 'after lunch'])
  expect(seen.submitted).toEqual(['[queue q3] after lunch'])
  await ui.unmount()
})

test('an answer the queue refuses is a toast and reaches no chat', async ($, on) => {
  const seen = engine(on, () => ({ ...TWO, open: [row('q9', 'Answered elsewhere', '1m', { options: ['Yes'] })] }))
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.press({ key: 'q9:0' })
  await new Promise(r => setTimeout(r, 50))
  expect(seen.submitted).toEqual([])
  expect(seen.toasts).toEqual(['Not sent: crius-queue: q9 is already answered'])
  await ui.unmount()
})

test('empty: "Nothing waits on you" in the pane and the status line, and no band', async ($, on) => {
  const seen = engine(on, () => EMPTY)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(seen.status.at(-1)).toBe('✓ nothing waits on you')
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect((await ui.find({ type: 'Text', text: /Nothing waits/ }))?.text).toBe('✓ Nothing waits on you.')
  await ui.unmount()
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await band.find({ type: 'Text', text: /waiting on you/ })).toBeUndefined()
  await band.unmount()
})

test('a failed read is said in the status line and the band, never shown as an empty list', async ($, on) => {
  const seen = engine(on, () => 'fail')
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(seen.status.at(-1)).toBe('⚑ owner queue FAILED: crius-queue: the owner queue was never set up')
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  expect(await ui.find({ type: 'Text', text: /Nothing waits/ })).toBeUndefined()
  expect((await ui.find({ type: 'Text', text: /could not be read/ }))?.text).toMatch(/never set up/)
  await ui.unmount()
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect((await band.find({ type: 'Text', text: /FAILED/ }))?.text).toMatch(/never set up/)
  await band.unmount()
})

test('the band names the count and the oldest item', async ($, on) => {
  engine(on, () => TWO)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect((await band.find({ type: 'Text', text: /waiting on you/ }))?.text).toBe('⚑ 2 waiting on you')
  expect((await band.find({ type: 'Text', text: /oldest/ }))?.text).toBe('oldest: Restart Chrome (4h)')
  await band.unmount()
})

test('/waiting prints the list, and "/waiting 2 any text" answers item 2', async ($, on) => {
  const seen = engine(on, () => TWO)
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const list = await $.command.run({ command: 'waiting', args: '' })
  expect(list.text).toMatch(/^Waiting on you \(2\), oldest first\n1 Restart Chrome · Mac · 4h\n/)
  // the number is the place in the list drawn: item 2 is q2, sent by its id
  const sent = await $.command.run({ command: 'waiting', args: '2 not before Monday' })
  expect(sent.text).toBe('Recorded; sending to Crius: [queue q2] not before Monday')
  expect(seen.submitted).toEqual([])
  await seen.tick()
  expect(seen.calls).toContainEqual(['answer', 'q2', '--by', 'owner', 'not before Monday'])
  expect(seen.submitted).toEqual(['[queue q2] not before Monday'])
  const none = await $.command.run({ command: 'waiting', args: '9 yes' })
  expect(none.text).toBe('No item 9 in the list; /waiting shows it.')
})

test('an answer the chat does not take is said: recorded, and the line to type', async ($, on) => {
  const seen = engine(on, () => TWO, 'S-CRIUS', 'a Stop hook stands')
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  const ui = await $.ui.mount({ ...PANE, surface: 'terminal' })
  await ui.press({ key: 'q2:0' })
  await new Promise(r => setTimeout(r, 50))
  expect(seen.calls).toContainEqual(['answer', 'q2', '--by', 'owner', 'Yes, start it'])
  expect(seen.toasts).toEqual(['Recorded, but not sent to Crius (a Stop hook stands). Type it yourself: [queue q2] Yes, start it'])
  await ui.unmount()
})

test('in any tab but Crius\'s the mod stays off: no pane, status, band or /waiting', async ($, on) => {
  const seen = engine(on, () => TWO, 'S-OTHER')
  await $.session.start({ cwd: '/', surface: 'terminal', isInteractive: true })
  expect(seen.status).toEqual([])
  expect(seen.opened).toEqual([])
  expect(seen.commands).toEqual([])
  expect(seen.calls).toEqual([])
  const band = await $.ui.mount({ ...BAND, surface: 'terminal' })
  expect(await band.find({ type: 'Text', text: /waiting on you/ })).toBeUndefined()
  await band.unmount()
})
