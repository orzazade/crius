package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)

func setup(t *testing.T, bus string) string {
	t.Helper()
	h := t.TempDir()
	for _, d := range []string{"bus", "state"} {
		if err := os.MkdirAll(filepath.Join(h, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(h, "bus", "cortex.log"), []byte(bus), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(h, now, []string{"init", "--since", "2026-10-30T00:00"}, &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	return h
}

func cli(t *testing.T, h string, args ...string) string {
	t.Helper()
	var b strings.Builder
	if err := run(h, now, args, &b); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return b.String()
}

// /waiting answers by number: "2 any text" answers the second row of the list, oldest first, and prints the line the
// mod submits to Crius.
func TestAnswerByNumberTakesTheListPlace(t *testing.T) {
	h := setup(t, "2026-10-30T09:00 NEED-OWNER cortex:lead -> crius — older one\n")
	cli(t, h, "add", "--title", "Restart Chrome", "--from", "Mac", "--options", "Done | Remind me at 18:00")
	if got := cli(t, h, "answer", "2", "not", "before", "Monday"); got != "[queue q2] not before Monday\n" {
		t.Fatalf("got %q", got)
	}
	var v View
	if err := json.Unmarshal([]byte(cli(t, h, "list", "--json")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Open) != 1 || v.Open[0].ID != "q1" || v.Open[0].Age != "5h" || len(v.Answered) != 1 || v.Answered[0].Answer != "not before Monday" {
		t.Fatalf("got %+v", v)
	}
	if err := run(h, now, []string{"answer", "5", "x"}, &strings.Builder{}); err == nil || !strings.Contains(err.Error(), "no item 5: 1 wait on you") {
		t.Fatalf("want a refusal for item 5, got %v", err)
	}
}

func TestAnEmptyListSaysNothingWaits(t *testing.T) {
	h := setup(t, "")
	if got := cli(t, h, "list"); got != "✓ Nothing waits on you.\n" {
		t.Fatalf("got %q", got)
	}
}
