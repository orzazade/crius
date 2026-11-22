package main

// A DECISION crius:lead posts on the AXI log that leans on Microsoft documentation names the page and its date: AXI
// decisions rested on old pages (an "old MS page" on userId, 11-22), and a reader of the bus could not tell which page or
// how old. The rule reads the whole text and each note file the text names (a .txt or .md in a notes folder, or
// with "note" in its name); the page URL and the date may stand in either. Other buses, senders and types are free of it, and so is a
// DECISION that names Microsoft but not its documentation.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const (
	docWord = `(?:docs?|documentation)\b(?:[^\w/-]|$)`
	msOwner = `(?:microsoft(?:'s|\x{2019}s)?|(?-i:MS|MSFT)|partner[\s-]center(?:'s|\x{2019}s)?)`
	// What may stand between the two ("Microsoft Graph API reference docs"); any other word is a thing of ours
	// ("Microsoft adapter docs"). sep takes markup too: Microsoft **docs**.
	between = `(?:(?:api|apis|rest|graph|partner|center|azure|official|online|public|developer|reference|current|latest|newest|old|older|new|own|sdk|csp|web|learn)[\s-]+)`
	sep     = `[\s*_"'` + "`" + `\x{201C}-]+`
	says    = `(?:says?|shows?|omits?|leaves?|lists?|states?|describes?|has|does)\b`
)

var (
	msDocs = regexp.MustCompile(`(?i)\b(?:learn|docs)\.microsoft\.com|\baka\.ms/|\b(?:microsoft(?:'s|\x{2019}s)?|(?-i:MS)) (?-i:Learn)\b|` +
		`\b` + msOwner + sep + between + `{0,4}` + docWord + `|(?:^|[^\s\d]\s+)ms ` + docWord + // not "250 ms docs"
		`|\b(?:docs?|documentation|documented)\s+(?:of|from|by|at|for)\s+(?:the\s+)?` + msOwner + `(?:$|[^\w\s-]|\s+(?:` + between + `*(?:api|apis|sdk|portal|` + says + `)))` +
		`|\bdocs?(?:\s+\(` + msOwner + `\)|:\s+` + msOwner + `\s+` + says + `)|\bmicrosoft documents\b` +
		`|\b(?:microsoft(?:'s|\x{2019}s)?|(?-i:MS)|partner[\s-]center)\s+` + between + `{0,4}(?:reference\s+)?pages?\s+` + says + `|\b(?:microsoft(?:'s|\x{2019}s)?|(?-i:MS))\s+page(?:$|[^\w\s])`)
	msPage = regexp.MustCompile(`(?i)https?://(?:learn|docs)\.microsoft\.com/[\w%.-]+/[\w%./-]*\w`)
	month  = `(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?`
	msDate = regexp.MustCompile(`(?i)\blast[ -]updated[:*_\s]+(?:on\s+)?(?:\d{4}-\d{2}-\d{2}|\d{1,2}[./]\d{1,2}[./]\d{4}|` +
		month + `\s+\d{1,2},?\s+\d{4}|\d{1,2}\s+` + month + `,?\s+\d{4})`)
	// A path as a text quotes it: bare, in brackets, quotes or backticks, or behind a colon (full:/x).
	notePath = regexp.MustCompile("(?:^|[\\s(\\[<\"'`:=])((?:~/|/[^/\\s])[^\\s,;·\"'`)\\]>]*)")
)

// namesMSDocs refuses a text (with its notes) that refers to Microsoft documentation without a page URL and the date the
// page was last updated. A note it cannot read counts as not there: it holds only a text that refers to
// Microsoft documentation itself, as many DECISIONs name a note that lies on another machine.
func namesMSDocs(text string) error {
	all := text
	var unread error
	for _, m := range notePath.FindAllStringSubmatch(text, -1) {
		p := strings.TrimRightFunc(m[1], func(r rune) bool { return unicode.IsPunct(r) && r != '/' })
		if ext := filepath.Ext(p); ext != ".txt" && ext != ".md" ||
			!strings.Contains(p, "/notes/") && !strings.Contains(strings.ToLower(filepath.Base(p)), "note") {
			continue
		}
		b, err := os.ReadFile(expand(p))
		if err != nil {
			unread = fmt.Errorf("could not read the note this DECISION names: %v", err)
			continue
		}
		all += "\n" + string(b)
	}
	if !msDocs.MatchString(all) {
		return nil
	}
	var missing []string
	if !msPage.MatchString(all) {
		missing = append(missing, "the page URL (https://learn.microsoft.com/…)")
	}
	if !msDate.MatchString(all) {
		missing = append(missing, "\"last updated <date>\" as that page shows it (last updated 2026-11-03)")
	}
	if len(missing) == 0 {
		return nil
	}
	err := errors.New("a DECISION to AXI that refers to Microsoft documentation names the page URL and \"last updated <date>\", " +
		"so a decision never rests on an old page unseen; add " + strings.Join(missing, " and ") + " to the text or the note it names")
	if unread != nil {
		err = fmt.Errorf("%w; %v", err, unread)
	}
	return err
}
