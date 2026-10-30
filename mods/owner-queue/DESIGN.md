# Owner queue: design

The mod draws one list: what waits on the owner. Approved mockup (owner 10-30, "sounds perfect the design"):
`~/.crius/state/crius/owner-queue-mockup.html`, screen 1 (pane beside the chat) and screen 2 (band and `/waiting`).
Owner update 10-30 ~14:00: all four places at once, and every item has its own free-text answer field.

## Where it shows

All four read the same queue (`crius-queue list --json`, every 5 s) and say the same count. They show in Crius's tab
alone (the cmux surface `~/.crius/roles/crius.lead.json` names); other sessions in the folder load the mod and stay off.
An off mod looks again every 5 s, since crius-hook may write the role file after the mod started.

| Place | What it shows |
| --- | --- |
| Pane beside the chat (`Waiting on you`) | head with count, cards oldest first, "Answered today" at the bottom |
| Status line | `⚑ N waiting on you`; `✓ nothing waits on you` at 0; `⚑ owner queue FAILED: …` when the read fails |
| Band above the prompt | `⚑ N waiting on you · oldest: <title> (<age>) · /waiting`; nothing at 0 |
| `/waiting` | the list as text; `/waiting 2 <any text>` answers item 2 of the list drawn now (sent by its id). When the list `/waiting` printed is no longer the one drawn, nothing is recorded and the list is printed again. A refused answer is said in the reply, not again as a toast |

The pane opens by itself where the terminal docks it (fullscreen, from about 110-144 columns). Below that it waits,
and the band and `/waiting` carry everything.

## A card

```
1  Restart Chrome                                Mac · 4h
   Do        Quit Chrome fully, then open it again.
   Why you   Crius never drives your Chrome.
   Unblocks  Browser checks
   [ Done ]  [ Remind me at 18:00 ]
   Your own answer…                                  send
```

- Number (accent), title (bold), `from · age` (dim) on one row.
- Label column 10 cells wide, labels dim; the Do text in the main colour, Why you and Unblocks in the soft colour.
  A part the row does not have is left out, never drawn empty.
- Buttons: the row's options; the first is the primary one (accent). A row with no options has none.
- The free-text field is always there, under the buttons.
- Pressing a button or Enter in the field records the answer (`crius-queue answer`) and submits one line into the
  Crius chat: `[queue q2] <answer>`. A second answer to the same row is refused by the queue and shown as a toast. An answer the chat does
  not take is a toast too: recorded, with the line to type by hand.

Empty: a box (round border, the box colour) with `✓ Nothing waits on you.` (in the ok colour) and, dim, "Crius adds a row here when a lead
needs your call."

## Colours (the mockup's tokens, drawn as raw colours)

| Token | Hex | Used for |
| --- | --- | --- |
| pane | `#1b1a18` | pane background |
| fg | `#e6e3da` | titles, Do text, field text |
| soft | `#c9c5bb` | Why you, Unblocks |
| dim | `#9a968c` | labels, `from · age`, "oldest first", the answered list |
| box | `#3a3935` | card border (not text) |
| accent | `#e08a63` | count badge background, primary button, band border |
| accent-fg | `#f0a988` | item number, `⚑ N waiting on you` |
| ok | `#8fc27e` | the ✓ of the empty state and of answered rows |
| badge text | `#161615` | the count on the accent badge |

Every text colour is measured at 4.5:1 or more on the pane background (see the live check in the DONE line).
`faint` (`#6f6c64`) from the mockup is 3.3:1 there, so it is not used for text.

## Type and layout

- The terminal has one monospaced face and one size; weight is bold or regular only.
- Pane body: column, 1 row between cards; head row, cards, a flexible gap, then "Answered today".
- Text wraps inside the card; titles longer than the row are cut with "…".

## Never use

- Emoji as icons. The only glyphs are `⚑` (waiting) and `✓` (done), both text characters.
- Colour as the only meaning: every state also has words or a glyph (count, `✓`, "Answered today").
- Red and green as a pair (the ok green never sits next to a red).
- Anything that blinks, spins or moves by itself; the count changes only when the queue does.
- The `faint` colour for text, and any text colour under 4.5:1 on the pane background.
- More than one primary button per card.
