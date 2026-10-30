// One row of the owner queue as `crius-queue list --json` prints it (cmd/crius-queue).
export type Item = {
  id: string
  title: string
  do?: string
  why?: string
  unblocks?: string
  options?: string[]
  from: string
  opened: string
  state: 'open' | 'answered'
  answer?: string
  answeredAt?: string
  age: string
}

export type View = { open: Item[]; answered: Item[]; now: string }

declare module 'claude-code' {
  interface PluginState {
    'owner-queue': {
      view: Shaped<View | null>
      error: string | null
      busy: string | null
      outbox: string[]
    }
  }
}
