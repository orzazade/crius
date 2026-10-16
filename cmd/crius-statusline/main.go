// crius-statusline is the statusLine command: it records a quota sample (design §5) and prints the quota line.
// A failure is printed in the statusline itself, with exit 0 because Claude Code shows nothing for a non-zero exit,
// so a broken sampler is seen at once; the ingest also alerts when open sessions have no sample for 45 minutes.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	persona := flag.String("persona", "", "personal or work: whose statusline this is")
	flag.Parse()
	if *persona != "personal" && *persona != "work" {
		fmt.Println("crius-statusline: FAILED, needs --persona personal|work")
		return
	}
	in, err := io.ReadAll(os.Stdin)
	if err == nil {
		var out string
		out, err = quota.Sample(store.Home(), *persona, in, time.Now())
		if err == nil {
			fmt.Println(out)
			return
		}
	}
	fmt.Printf("crius quota sample FAILED: %v\n", err)
}
