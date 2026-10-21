// Command crius-wakewatch runs one missed-wake check (internal/wakewatch) and exits 1 when it could not finish or not
// reach the owner. launchd starts it every 5 min through bin/crius-wakewatch-run, which hands it TELEGRAM_BOT_TOKEN
// and TELEGRAM_CHAT_ID from ~/.secrets.
package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
)

var surfaceRe = regexp.MustCompile(`surface (surface:\d+) \[terminal\] "(.*)"`)

func main() {
	err := wakewatch.Run(wakewatch.Deps{Home: store.Home(), Now: time.Now(), Screens: screens, Send: send})
	if err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch:", err)
		os.Exit(1)
	}
}

// screens reads the last 30 lines of every terminal tab in cmux.
func screens() ([]wakewatch.Screen, error) {
	tree, err := exec.Command("cmux", "tree", "--all").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("cmux tree: %v: %s", err, strings.TrimSpace(string(tree)))
	}
	var out []wakewatch.Screen
	for _, m := range surfaceRe.FindAllStringSubmatch(string(tree), -1) {
		text, err := exec.Command("cmux", "read-screen", "--surface", m[1], "--lines", "30").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("cmux read-screen %s: %v: %s", m[1], err, strings.TrimSpace(string(text)))
		}
		out = append(out, wakewatch.Screen{Surface: m[1], Title: m[2], Text: string(text)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cmux tree lists no terminal tab")
	}
	return out, nil
}

func send(text string) error {
	token, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_CHAT_ID")
	if token == "" || chat == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID is empty (read from ~/.secrets by crius-wakewatch-run)")
	}
	c := http.Client{Timeout: 15 * time.Second}
	resp, err := c.PostForm("https://api.telegram.org/bot"+token+"/sendMessage", url.Values{"chat_id": {chat}, "text": {text}})
	if err != nil {
		// The error quotes the URL, and the URL holds the token.
		return fmt.Errorf("telegram send failed: %s", strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram answered %s", resp.Status)
	}
	return nil
}
