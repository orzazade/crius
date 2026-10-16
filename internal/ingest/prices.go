package ingest

// List prices ($ per million tokens: input, output), copied from tools/agent-cost review-1016 extract.py.

import (
	"math"
	"strings"
)

var prices = []struct {
	prefix  string
	in, out float64
}{
	{"claude-opus-5-5", 4, 20}, {"claude-opus-5", 5, 25}, {"claude-opus-4", 5, 25}, {"claude-sonnet-5-5", 2, 10},
	{"claude-sonnet-5", 2, 10}, {"claude-sonnet-4", 3, 15}, {"claude-haiku-5-5", .1, .5}, {"claude-haiku-4-5", 1, 5},
	{"claude-fable-5", 10, 50}, {"claude-mythos-5", 10, 50},
}

// ListCost is the list-price $ of one request; ok is false for an unknown model (counted, never guessed).
func ListCost(model string, in, cr, cw, cw1h, cw5m, out int64) (cost float64, ok bool) {
	best := -1
	for i, p := range prices {
		if strings.HasPrefix(model, p.prefix) && (best < 0 || len(p.prefix) > len(prices[best].prefix)) {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	pin, pout := prices[best].in, prices[best].out
	if strings.HasPrefix(model, "claude-haiku-5-5") && in+cr+cw > 100000 {
		pin, pout = .5, 2.5
	}
	c1, c5 := cw1h, cw5m
	if c1 == 0 && c5 == 0 {
		c5 = cw
	} else if c1+c5 < cw {
		c5 += cw - c1 - c5
	}
	usd := (float64(in)*pin + float64(cr)*pin*.1 + float64(c1)*pin*2 + float64(c5)*pin*1.25 + float64(out)*pout) / 1e6
	return math.Round(usd*1e6) / 1e6, true
}
