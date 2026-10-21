package main

import (
	"errors"
	"strings"
	"testing"
)

// The send error must never carry the bot token, not even escaped: a token with \r is quoted as `\r` in a URL error.
func TestASendErrorNeverHoldsTheToken(t *testing.T) {
	for _, token := range []string{"123456:SECRET\rPART", "123456:SECRET PART\x7f"} {
		t.Setenv("TELEGRAM_BOT_TOKEN", token)
		t.Setenv("TELEGRAM_CHAT_ID", "42")
		err := send("hi")
		if err == nil {
			t.Fatalf("%q: no error", token)
		}
		for _, part := range []string{"123456", "SECRET", "PART"} {
			if strings.Contains(err.Error(), part) {
				t.Errorf("%q: error holds %q: %v", token, part, err)
			}
		}
	}
}

// One tab that cannot be read must not hide the others.
func TestOneFailedTabReadKeepsTheOthers(t *testing.T) {
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
