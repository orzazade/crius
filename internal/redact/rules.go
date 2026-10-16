package redact

import (
	_ "embed"
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// The gitleaks rules for token formats (step 4b), vendored from rules/gitleaks.toml by bin/refresh-gitleaks-rules;
// the JSON header names the version, commit and the TOML's sha256. MIT, see rules/LICENSE.gitleaks.
//
//go:embed rules/gitleaks.json
var gitleaksJSON []byte

type rule struct {
	id       string
	re       *regexp.Regexp
	group    int
	entropy  float64
	keywords []string
}

// rules fail the program at start when the vendored file does not load: the rules test covers it before a build.
var rules = loadRules(gitleaksJSON)

func loadRules(b []byte) []rule {
	var f struct {
		Rules []struct {
			ID, Regex   string
			SecretGroup int
			Entropy     float64
			Keywords    []string
		}
	}
	if err := json.Unmarshal(b, &f); err != nil {
		panic("redact: gitleaks rules: " + err.Error())
	}
	out := make([]rule, 0, len(f.Rules))
	for _, r := range f.Rules {
		out = append(out, rule{id: r.ID, re: regexp.MustCompile(r.Regex), group: r.SecretGroup, entropy: r.Entropy,
			keywords: r.Keywords})
	}
	return out
}

// redactRules replaces each secret a gitleaks rule finds: the rule's secret group, else its first non-empty group,
// else the whole match. A rule with an entropy floor skips a lower-entropy secret, as gitleaks does; its keywords
// are a quick filter, as in gitleaks.
func redactRules(s string) (string, int) {
	hits := 0
	lower := strings.ToLower(s)
	for _, r := range rules {
		if !hasKeyword(lower, r.keywords) {
			continue
		}
		ms := r.re.FindAllStringSubmatchIndex(s, -1)
		if ms == nil {
			continue
		}
		var b strings.Builder
		last := 0
		for _, m := range ms {
			lo, hi := m[0], m[1]
			if g := r.group; g > 0 && 2*g+1 < len(m) && m[2*g] >= 0 {
				lo, hi = m[2*g], m[2*g+1]
			} else if g == 0 {
				for i := 2; i+1 < len(m); i += 2 {
					if m[i] >= 0 && m[i+1] > m[i] {
						lo, hi = m[i], m[i+1]
						break
					}
				}
			}
			if lo < last || r.entropy > 0 && entropy(s[lo:hi]) < r.entropy {
				continue
			}
			b.WriteString(s[last:lo])
			b.WriteString("[redacted]")
			hits++
			last = hi
		}
		if last > 0 {
			b.WriteString(s[last:])
			s = b.String()
		}
	}
	return s, hits
}

func hasKeyword(lower string, kws []string) bool {
	if len(kws) == 0 {
		return true
	}
	for _, k := range kws {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// entropy is the Shannon entropy of s in bits per byte, as gitleaks measures it.
func entropy(s string) float64 {
	var n [256]int
	for i := 0; i < len(s); i++ {
		n[s[i]]++
	}
	e := 0.0
	for _, c := range n {
		if c > 0 {
			p := float64(c) / float64(len(s))
			e -= p * math.Log2(p)
		}
	}
	return e
}
