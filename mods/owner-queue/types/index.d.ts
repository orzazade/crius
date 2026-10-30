// One row of the owner queue as `crius-queue list --json` prints it (cmd/crius-queue).
export type Item = {
  id: string
  title: string
  do?: string
  why?: string
  unblocks?: string
  detail?: string // what a GOAL ask points to (a file section, a label), read in when the row opened
  options?: string[]
  from: string
  opened: string
  state: 'open' | 'answered'
  answer?: string
  answeredAt?: string
  asked?: string // the owner's last answer that asked back: the card stays open
  askedAt?: string
  age: string
}

// unread: each lead whose GOAL block crius-queue could not read, as "<lead>: <why>"; held: each GOAL ask held back,
// as "<lead>: <why>" (the owner may have answered it already)
export type View = { open: Item[]; answered: Item[]; now: string; unread?: string[]; held?: string[] }

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
