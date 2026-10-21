package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCmux puts a cmux on PATH that records each call, one per line, and gives Crius's tab in a config file.
func fakeCmux(t *testing.T, tab string) (log string) {
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '[%s]' \"$a\" >> " + log + "; done\necho >> " + log + "\necho OK\n"
	if err := os.WriteFile(filepath.Join(dir, "cmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	if tab != "" {
		if err := os.WriteFile(filepath.Join(home, "crius-tab"), []byte(tab), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

// Owner 10-21 23:5x: no Telegram; an alert is one line typed into Crius's own tab, then Enter.
func TestSendTypesTheLineIntoCriusTab(t *testing.T) {
	log := fakeCmux(t, "# Crius's tab\nworkspace:1000020000 surface:1000020029\n")
	if err := send("WAKEWATCH: quotes.log: DONE quotes:lead -> crius:lead — F-86"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "[send][--workspace][workspace:1000020000][--surface][surface:1000020029][--][WAKEWATCH: quotes.log: DONE quotes:lead -> crius:lead — F-86]\n" +
		"[send-key][--workspace][workspace:1000020000][--surface][surface:1000020029][enter]\n"
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
}

// With no tab named, nothing is typed anywhere (a bare cmux send would go to the caller's own tab) and it is loud.
func TestSendWithNoCriusTabFailsLoudly(t *testing.T) {
	for _, tab := range []string{"", "surface:1000020029\n", "workspace:1 surface:2 extra\n", "workspace:x surface:2\n"} {
		log := fakeCmux(t, tab)
		err := send("WAKEWATCH: x")
		if err == nil || !strings.Contains(err.Error(), "crius-tab") {
			t.Fatalf("%q: %v", tab, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Fatalf("%q: cmux was called", tab)
		}
	}
}

// One tab that cannot be read must not hide the others.
func TestOneFailedTabReadKeepsTheOthers(t *testing.T) {
	real := cmux
	t.Cleanup(func() { cmux = real })
	cmux = func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "tree":
			return []byte(`surface surface:1 [terminal] "quotes · lead"` + "\n" + `surface surface:2 [terminal] "axi · lead"` + "\n"), nil
		case args[2] == "surface:2":
			return []byte("surface gone"), errors.New("exit status 1")
		}
		return []byte("question?\n❓\n"), nil
	}
	got, err := screens()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "question?\n❓\n" || got[0].Err != "" || !strings.Contains(got[1].Err, "surface gone") {
		t.Fatalf("%+v", got)
	}
}

func TestSwapUsedIsReadInGB(t *testing.T) {
	for in, want := range map[string]float64{
		"total = 17408.00M  used = 15997.06M  free = 1410.94M  (encrypted)\n": 15997.06 / 1024,
		"total = 30.00G  used = 21.50G  free = 8.50G  (encrypted)\n":          21.5,
		"total = 0.00M  used = 0.00M  free = 0.00M\n":                         0,
	} {
		got, err := swapUsed(in)
		if err != nil || got < want-0.001 || got > want+0.001 {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := swapUsed("vm.swapusage: unknown oid"); err == nil {
		t.Error("an unreadable answer gave no error")
	}
}
