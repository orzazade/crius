// Command run puts fakeprompt's box in a real terminal, for a live check of crius-tsend in a throwaway tmux pane:
//
//	tmux new -d -s tsend-test ./fakeprompt
//
// It reads the terminal raw, so each read is what tmux wrote in one go, and redraws the box after each.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/orzazade/crius/internal/tabsend/fakeprompt"
)

func main() {
	stty := exec.Command("stty", "raw", "-echo")
	stty.Stdin = os.Stdin
	if out, err := stty.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "stty: %v: %s\n", err, out)
		os.Exit(1)
	}
	m := fakeprompt.New()
	draw := func() {
		fmt.Print("\x1b[H\x1b[2J" + strings.ReplaceAll(m.Screen(), "\n", "\r\n"))
	}
	draw()
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			return
		}
		if n == 1 && buf[0] == 0x03 { // C-c alone ends it
			return
		}
		m.Feed(buf[:n])
		draw()
	}
}
