package outbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// box is one fake Claude tab: cmux send refuses over a draft, as the real one does over typed text, and Enter moves the
// draft into typed.
type box struct {
	mu           sync.Mutex
	draft, title string
	typed        []string
	blind        int    // reads that fail now
	blindNext    int    // reads that fail after the next Enter, as when a dialog or redraw hides the box
	failOpen     bool   // send types onto a draft, as a cmux that cannot tell typed text would
	key          string // "fail": send-key fails and the key does not go; "lost": it goes nowhere, with no error;
	// "timeout": it goes, then cmux is killed; "timeout-lost": cmux is killed before it goes
	before  func(cmd string) // runs before each cmux call, as when the disk fails under it
	readErr string           // read-screen fails with this output
	// grey is faint text (a suggestion) shown while the box is empty; read-screen gives it as plain text, and the input
	// state calls the box empty. greyNext is shown after the next Enter. stateOf answers the input state for another tab.
	grey, greyNext, stateOf string
}

// fake replaces Cmux with tab and puts a ps on PATH that shows Claude in front.
func fake(t *testing.T, tab *box) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\necho '7 7 /x/claude'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old, oldWait := Cmux, BoxWait
	t.Cleanup(func() { Cmux, BoxWait = old, oldWait })
	BoxWait = time.Millisecond
	Cmux = func(args ...string) ([]byte, error) {
		tab.mu.Lock()
		defer tab.mu.Unlock()
		if tab.before != nil {
			tab.before(args[0])
		}
		killed := fmt.Errorf("%w after 30s: signal: killed", errKilled)
		switch {
		case args[0] == "--json":
			return []byte(`{"windows":[{"workspaces":[{"ref":"workspace:1","panes":[{"surfaces":[` +
				`{"ref":"surface:1","id":"S1","title":"` + tab.title + `","tty":"ttys015","type":"terminal"}]}]}]}]}`), nil
		case args[0] == "read-screen" && tab.readErr != "":
			return []byte(tab.readErr), errors.New("exit status 1")
		case args[0] == "read-screen" && tab.blind > 0:
			tab.blind--
			return []byte("Error: surface busy"), errors.New("exit status 1")
		case args[0] == "read-screen" && tab.draft == "":
			return []byte("⏺ done.\n──────────────\n❯ " + tab.grey + "\n──────────────\n"), nil
		case args[0] == "read-screen":
			return []byte("⏺ done.\n──────────────\n❯ " + tab.draft + "\n──────────────\n"), nil
		case args[0] == "rpc":
			id, state := "S1", "empty"
			if tab.stateOf != "" {
				id = tab.stateOf
			}
			if tab.draft != "" {
				state = "draft"
			}
			return []byte(`{"surface_id":"` + id + `","state":"` + state + `"}`), nil
		case args[0] == "send":
			if tab.draft != "" && !tab.failOpen {
				return []byte("Error: send: the agent prompt holds text someone is typing"), errors.New("exit status 1")
			}
			tab.draft += args[len(args)-1]
		case args[0] == "send-key" && tab.key == "fail":
			return []byte("Error: surface not found"), errors.New("exit status 1")
		case args[0] == "send-key" && tab.key == "timeout-lost":
			return nil, killed
		case args[0] == "send-key" && tab.key == "timeout":
			tab.typed = append(tab.typed, tab.draft)
			tab.draft = ""
			return nil, killed
		case args[0] == "send-key" && tab.key == "lost":
		case args[0] == "send-key":
			tab.typed = append(tab.typed, tab.draft)
			tab.draft = ""
			tab.grey, tab.greyNext = tab.greyNext, ""
			tab.blind, tab.blindNext = tab.blindNext, 0
		}
		return nil, nil
	}
}

// Two senders at once (wakewatch and crius-say) must not type one stored message twice or lose one.
func TestSendersAtOnceTypeEachMessageOnce(t *testing.T) {
	tab := &box{title: "quotes · lead"}
	fake(t, tab)
	home := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Send(home, tab.title, fmt.Sprintf("message %d", i)); err != nil {
				t.Errorf("Send %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	seen := map[string]int{}
	for _, s := range tab.typed {
		seen[s]++
	}
	for i := 0; i < 8; i++ {
		if m := fmt.Sprintf("message %d", i); seen[m] != 1 {
			t.Errorf("%q typed %d times, want once; typed %q", m, seen[m], tab.typed)
		}
	}
	if left, _ := list(home); len(left) != 0 {
		t.Errorf("%d messages left in the outbox, want none", len(left))
	}
}

// Text someone typed holds the message, stored; Flush types it once the box is empty.
func TestADraftHoldsTheMessageUntilFlush(t *testing.T) {
	tab := &box{title: "crius", draft: "half a thought"}
	fake(t, tab)
	home := t.TempDir()
	err := Send(home, "crius", "WAKEWATCH: one")
	if err == nil || !strings.Contains(err.Error(), "held, 1 message(s)") {
		t.Fatalf("Send over a draft: err %v, want held", err)
	}
	if len(tab.typed) != 0 || tab.draft != "half a thought" {
		t.Fatalf("typed %q, draft %q: the owner's draft must stay as it was", tab.typed, tab.draft)
	}
	left, _ := list(home)
	if len(left) != 1 || left[0].Tries != 1 || left[0].LastError == "" {
		t.Fatalf("stored %+v, want one message with one try and its error", left)
	}
	tab.draft = ""
	if _, err := Flush(home); err != nil {
		t.Fatal(err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "WAKEWATCH: one" {
		t.Errorf("typed %q, want the held message once", tab.typed)
	}
	if left, _ := list(home); len(left) != 0 {
		t.Errorf("%d messages left after Flush, want none", len(left))
	}
}

// Review 10-22: an Enter that worked but whose check failed (the box hidden for a moment) left the message stored, and
// the next try typed it again into the now empty box.
func TestAMessageWhoseEnterWentIsNotTypedAgain(t *testing.T) {
	tab := &box{title: "quotes · lead"}
	fake(t, tab)
	home := t.TempDir()
	tab.blindNext = 20 // every read of the after-Enter check fails
	if err := Send(home, tab.title, "merge it"); err == nil {
		t.Fatal("Send: no error, want the failed after-Enter check")
	}
	if _, err := Flush(home); err != nil {
		t.Fatal(err)
	}
	if len(tab.typed) != 1 || tab.draft != "" {
		t.Errorf("typed %q, draft %q: want the message typed once", tab.typed, tab.draft)
	}
	if left, _ := list(home); len(left) != 0 {
		t.Errorf("%d messages left, want none", len(left))
	}
}

// Review 10-22: a message for a tab that is gone stayed forever and was typed into a later tab with the same title.
func TestAMessageOlderThanMaxAgeIsSetAsideNotTyped(t *testing.T) {
	tab := &box{title: "quotes · lead"}
	fake(t, tab)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-MaxAge - time.Minute)
	if err := write(filepath.Join(home, "outbox", fmt.Sprintf("%020d-00000000.json", old.UnixNano())),
		Message{To: tab.title, Text: "merge it", Created: old}); err != nil {
		t.Fatal(err)
	}
	aside, err := Flush(home)
	if err != nil || len(aside) == 0 || !strings.Contains(fmt.Sprint(aside), "never typed") || !strings.Contains(fmt.Sprint(aside), "merge it") {
		t.Fatalf("Flush: set aside %v, err %v, want the message named as set aside", aside, err)
	}
	if len(tab.typed) != 0 {
		t.Errorf("typed %q, want nothing", tab.typed)
	}
	if left, _ := list(home); len(left) != 0 {
		t.Errorf("%d messages left, want none", len(left))
	}
	if kept, _ := filepath.Glob(filepath.Join(home, "outbox", "dropped", "*.json")); len(kept) != 1 {
		t.Errorf("dropped/ holds %q, want the old message", kept)
	}
}

// Review 10-22: one file that does not parse stopped every send and flush, for every tab. Re-review 10-22: and a send
// that went came back as an error naming that file, so a caller that tries again on an error typed it twice. Send
// leaves such files to Flush, which sets them aside and says so apart from the messages it holds.
func TestADamagedFileIsSetAsideAndTheRestStillGo(t *testing.T) {
	tab := &box{title: "crius"}
	fake(t, tab)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "outbox", "00000000000000000001-00000000.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-MaxAge - time.Minute)
	if err := write(filepath.Join(home, "outbox", fmt.Sprintf("%020d-00000000.json", old.UnixNano())),
		Message{To: "crius", Text: "stale", Created: old}); err != nil {
		t.Fatal(err)
	}
	if err := Send(home, "crius", "WAKEWATCH: one"); err != nil {
		t.Errorf("Send: %v, want nil: the message went", err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "WAKEWATCH: one" {
		t.Errorf("typed %q, want the new message only", tab.typed)
	}
	aside, err := Flush(home)
	if err != nil || len(aside) == 0 || !strings.Contains(fmt.Sprint(aside), "damaged") || !strings.Contains(fmt.Sprint(aside), "stale") {
		t.Errorf("Flush: set aside %v, err %v, want the damaged file and the old message named", aside, err)
	}
	if kept, _ := filepath.Glob(filepath.Join(home, "outbox", "damaged", "*.json")); len(kept) != 1 {
		t.Errorf("damaged/ holds %q, want the bad file", kept)
	}
}

// Re-review 10-22: a send-key that failed left Entered set, so a box found empty later counted the message as sent.
func TestAnEnterThatFailedIsNotTakenAsSent(t *testing.T) {
	tab := &box{title: "crius", key: "fail"}
	fake(t, tab)
	home := t.TempDir()
	if err := Send(home, "crius", "merge it"); !errors.Is(err, ErrHeld) {
		t.Fatalf("Send: err %v, want it held", err)
	}
	if left, _ := list(home); len(left) != 1 || left[0].Entered {
		t.Fatalf("stored %+v, want the message with no Enter recorded", left)
	}
	tab.key, tab.draft = "", "" // the box was cleared
	if _, err := Flush(home); err != nil {
		t.Fatal(err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "merge it" {
		t.Errorf("typed %q, want the message once", tab.typed)
	}
}

// Re-review 10-22: an Enter that went nowhere said "after Enter: <nil>".
func TestAnEnterThatLeftTheTextSaysSo(t *testing.T) {
	tab := &box{title: "crius", key: "lost"}
	fake(t, tab)
	err := Send(t.TempDir(), "crius", "merge it")
	if err == nil || strings.Contains(err.Error(), "<nil>") || !strings.Contains(err.Error(), "still holds the message after Enter") {
		t.Errorf("Send: err %v, want the box named as still holding it, with no <nil>", err)
	}
}

// Re-review 10-22: crius-say must tell "stored, wakewatch types it later" and "in the box, set aside" from "not
// stored": a retry is right only for the last.
func TestAHeldOrTypedOntoMessageIsMarked(t *testing.T) {
	tab := &box{title: "crius", draft: "half a thought"}
	fake(t, tab)
	if err := Send(t.TempDir(), "crius", "one"); !errors.Is(err, ErrHeld) {
		t.Errorf("over a draft: err %v, want ErrHeld", err)
	}
	tab.failOpen = true
	if err := Send(t.TempDir(), "crius", "two"); !errors.Is(err, ErrOnto) || errors.Is(err, ErrHeld) {
		t.Errorf("typed onto a draft: err %v, want ErrOnto and not ErrHeld", err)
	}
}

// Re-review 10-22: a message whose Enter went but whose check after failed was reported as never typed when it aged out.
func TestAnAgedMessageWhoseEnterWentSaysSo(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-MaxAge - time.Minute)
	if err := write(filepath.Join(home, "outbox", fmt.Sprintf("%020d-00000000.json", old.UnixNano())),
		Message{To: "crius", Text: "merge it", Created: old, Entered: true}); err != nil {
		t.Fatal(err)
	}
	aside, _ := Flush(home)
	if len(aside) == 0 || strings.Contains(fmt.Sprint(aside), "never typed") || !strings.Contains(fmt.Sprint(aside), "Enter was pressed") {
		t.Errorf("set aside %v, want it to say Enter was pressed", aside)
	}
}

// Review 10-22: a cmux that typed onto someone's text (its input-state check failing open) would get the same message
// again on every later try. The message is set aside after the first.
func TestAMessageTypedOntoADraftIsNotTypedAgain(t *testing.T) {
	tab := &box{title: "crius", draft: "half a thought", failOpen: true}
	fake(t, tab)
	home := t.TempDir()
	err := Send(home, "crius", "WAKEWATCH: one")
	if err == nil || !strings.Contains(err.Error(), "typed onto") {
		t.Fatalf("Send: err %v, want one saying it typed onto the draft", err)
	}
	if _, err := Flush(home); err != nil {
		t.Fatal(err)
	}
	if tab.draft != "half a thoughtWAKEWATCH: one" || len(tab.typed) != 0 {
		t.Errorf("draft %q, typed %q: want the message typed once and no Enter", tab.draft, tab.typed)
	}
}

// Review 10-22: a sender stuck on a hung cmux held the lock, and wakewatch waited on it for ever.
func TestAHeldLockFailsInsteadOfWaitingForEver(t *testing.T) {
	home := t.TempDir()
	unlock, err := lock(home)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	oldWait := LockWait
	t.Cleanup(func() { LockWait = oldWait })
	LockWait = 50 * time.Millisecond
	done := make(chan error, 1)
	go func() { _, err := Flush(home); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "locked") {
			t.Errorf("Flush: err %v, want locked", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Flush still waits on the lock after 5s")
	}
}

// Review 10-22: a cmux that hangs held the outbox lock for ever. A call that runs past CmuxTimeout is killed.
func TestAHungCmuxIsKilled(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cmux"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old := CmuxTimeout
	t.Cleanup(func() { CmuxTimeout = old })
	CmuxTimeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := runCmux("tree"); err == nil {
		t.Error("runCmux: no error from a cmux that hangs")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("runCmux took %v, want about CmuxTimeout", d)
	}
}

// Review 10-22: a failed read-screen put the screen into the error, which reaches the bus and the stored message.
func TestAFailedReadKeepsSecretsOutOfTheError(t *testing.T) {
	tab := &box{title: "crius", readErr: "partial screen: export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789"}
	fake(t, tab)
	home := t.TempDir()
	err := Send(home, "crius", "WAKEWATCH: one")
	if err == nil || strings.Contains(err.Error(), "ghp_abcdef") {
		t.Errorf("Send: err %v, want an error without the token", err)
	}
	left, _ := list(home)
	if len(left) != 1 || strings.Contains(left[0].LastError, "ghp_abcdef") {
		t.Errorf("stored %+v, want the message with no token in its error", left)
	}
}

// Re-review 2, 10-22: an Enter whose cmux was killed after the key went was taken as an Enter that surely failed, and
// the next run typed the message again into the empty box.
func TestATimedOutEnterThatWentIsNotTypedAgain(t *testing.T) {
	tab := &box{title: "crius", key: "timeout"}
	fake(t, tab)
	home := t.TempDir()
	if err := Send(home, "crius", "merge it"); !errors.Is(err, ErrHeld) {
		t.Fatalf("Send: err %v, want it held", err)
	}
	tab.key = ""
	if _, err := Flush(home); err != nil {
		t.Fatal(err)
	}
	if len(tab.typed) != 1 {
		t.Errorf("typed %q, want the message once", tab.typed)
	}
}

// Re-review 2, 10-22: after an Enter that may not have gone, a box holding the message with more typed around it was
// taken as "sent", and the message dropped without a word.
func TestATimedOutEnterUnderNewTextIsSetAside(t *testing.T) {
	tab := &box{title: "crius", key: "timeout-lost"}
	fake(t, tab)
	home := t.TempDir()
	if err := Send(home, "crius", "merge it"); !errors.Is(err, ErrHeld) {
		t.Fatalf("Send: err %v, want it held", err)
	}
	tab.key, tab.draft = "", tab.draft+" and the owner's words"
	aside, err := Flush(home)
	if err != nil || !strings.Contains(fmt.Sprint(aside), "typed onto") || !strings.Contains(fmt.Sprint(aside), "merge it") {
		t.Errorf("Flush: set aside %v, err %v, want the message named as typed onto other text", aside, err)
	}
	if len(tab.typed) != 0 {
		t.Errorf("typed %q, want no Enter on the owner's words", tab.typed)
	}
}

// Re-review 2, 10-22: a cmux that exits 0 but leaves a child holding its pipes was taken as failed, so a send-key that
// went counted as one that did not.
func TestACmuxThatExitedCleanIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cmux"), []byte("#!/bin/sh\nsleep 3 &\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if out, err := runCmux("send-key", "enter"); err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Errorf("runCmux: out %q, err %v, want ok and no error", out, err)
	}
}

// Re-review 2, 10-22: a held message typed onto someone's text by a later sender's Send was set aside, and only that
// sender heard of it: Crius never did, though the message's own sender had gone.
func TestAnOlderMessageTypedOntoTextInASendReachesFlush(t *testing.T) {
	tab := &box{title: "crius", draft: "half a thought"}
	fake(t, tab)
	home := t.TempDir()
	if err := Send(home, "crius", "one"); !errors.Is(err, ErrHeld) {
		t.Fatalf("first Send: err %v, want it held", err)
	}
	tab.failOpen = true
	if err := Send(home, "crius", "two"); !errors.Is(err, ErrOnto) {
		t.Fatalf("second Send: err %v, want the first message typed onto the draft", err)
	}
	aside, _ := Flush(home)
	if !strings.Contains(fmt.Sprint(aside), "typed onto") || !strings.Contains(fmt.Sprint(aside), `"one"`) {
		t.Errorf("Flush: set aside %v, want the first message named", aside)
	}
}

// Re-review 2, 10-22: a sent message whose file could not be removed, or a held one whose try could not be saved,
// came back as a plain error, which crius-say reads as "not stored, send it again".
func TestAStoredMessageIsNeverReportedAsNotStored(t *testing.T) {
	block := func(home string) func(string) {
		return func(string) {
			paths, _ := filepath.Glob(filepath.Join(home, "outbox", "*.json"))
			for _, p := range paths {
				os.Remove(p)
				os.MkdirAll(filepath.Join(p, "x"), 0o700) // a folder that is not empty cannot be removed or replaced
			}
		}
	}
	tab := &box{title: "crius"}
	fake(t, tab)
	home := t.TempDir()
	b := block(home)
	tab.before = func(cmd string) {
		if cmd == "send-key" {
			b(cmd)
		}
	}
	if err := Send(home, "crius", "sent"); !errors.Is(err, ErrHeld) {
		t.Errorf("sent, file not removed: err %v, want ErrHeld", err)
	}
	tab2 := &box{title: "crius", draft: "half a thought"}
	fake(t, tab2)
	home2 := t.TempDir()
	b2 := block(home2)
	tab2.before = func(cmd string) {
		if cmd == "send" {
			b2(cmd)
		}
	}
	if err := Send(home2, "crius", "held"); !errors.Is(err, ErrHeld) {
		t.Errorf("held, try not saved: err %v, want ErrHeld", err)
	}
}

// Re-review 4, 10-22: grey text that matched the message (a suggestion "continue") was taken as the message typed
// earlier, so only Enter was pressed and the message never typed.
func TestAGreySuggestionLikeTheMessageIsNotTakenForIt(t *testing.T) {
	tab := &box{title: "crius", grey: "continue"}
	fake(t, tab)
	if err := Send(t.TempDir(), "crius", "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "continue" {
		t.Errorf("typed %q, want the message typed and sent once", tab.typed)
	}
}

// Re-review 4, 10-22: a suggestion like the message, shown after its Enter, failed the check after Enter, and the next
// run pressed Enter on it again.
func TestASuggestionLikeASentMessageIsNotSentAgain(t *testing.T) {
	tab := &box{title: "crius", greyNext: "continue"}
	fake(t, tab)
	home := t.TempDir()
	if err := Send(home, "crius", "continue"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if aside, err := Flush(home); err != nil || len(aside) != 0 {
		t.Fatalf("Flush: set aside %v, err %v", aside, err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "continue" {
		t.Errorf("typed %q, want the message sent once", tab.typed)
	}
}

// Re-review 4, 10-22: cmux answers the input state of another tab for an id it does not know. That answer must not
// decide what is pressed here.
func TestAnInputStateForAnotherTabHoldsTheMessage(t *testing.T) {
	tab := &box{title: "crius", grey: "continue", stateOf: "S2"}
	fake(t, tab)
	err := Send(t.TempDir(), "crius", "continue")
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "S2") {
		t.Errorf("Send: err %v, want it held, naming the other tab", err)
	}
	if len(tab.typed) != 0 {
		t.Errorf("typed %q, want nothing", tab.typed)
	}
}

// Re-review 4, 10-22: a message typed onto text whose mark could not be saved was set aside at once, and Flush, which
// leaves typed-onto messages to its sweep, never named it.
func TestATypedOntoMessageThatCouldNotBeMarkedIsNamed(t *testing.T) {
	tab := &box{title: "crius", draft: "half a thought"}
	fake(t, tab)
	home := t.TempDir()
	if err := Send(home, "crius", "one"); !errors.Is(err, ErrHeld) {
		t.Fatalf("Send: err %v, want it held", err)
	}
	tab.failOpen = true
	tab.before = func(cmd string) {
		if cmd != "send" {
			return
		}
		paths, _ := filepath.Glob(filepath.Join(home, "outbox", "*.json"))
		for _, p := range paths {
			os.MkdirAll(filepath.Join(p+".tmp", "x"), 0o700) // a folder that is not empty: the mark cannot be written
		}
	}
	aside, _ := Flush(home)
	if !strings.Contains(fmt.Sprint(aside), "typed onto") || !strings.Contains(fmt.Sprint(aside), `"one"`) {
		t.Errorf("Flush: set aside %v, want the message named", aside)
	}
	if kept, _ := filepath.Glob(filepath.Join(home, "outbox", "dropped", "*.json")); len(kept) != 1 {
		t.Errorf("dropped/ holds %q, want the message", kept)
	}
}

// Re-review 5, 10-22: the screen read and the typed-or-grey check are two looks. Grey text like the message, then the
// owner typing before the check, read as the message typed: Enter sent the owner's half word.
func TestTextTypedBetweenTheTwoLooksIsNotSent(t *testing.T) {
	tab := &box{title: "crius", grey: "continue"}
	fake(t, tab)
	tab.before = func(cmd string) {
		if cmd == "rpc" {
			tab.draft = "co"
		}
	}
	if err := Send(t.TempDir(), "crius", "continue"); !errors.Is(err, ErrHeld) {
		t.Errorf("Send: err %v, want it held", err)
	}
	if len(tab.typed) != 0 || tab.draft != "co" {
		t.Errorf("typed %q, draft %q: want the owner's text left alone", tab.typed, tab.draft)
	}
}
