package ingest

// List prices ($ per million tokens: input, output; cache read as a share of input), from the claude-api
// pricing reference cached 2026-10-06. A model it does not price is left unpriced, never guessed.

import (
	"crypto/sha1"
	"database/sql"
	"fmt"
	"math"
	"strings"
)

var prices = []struct {
	prefix      string
	in, out, cr float64
}{
	{"claude-opus-5-5", 4, 20, .05}, {"claude-opus-5", 5, 25, .1}, {"claude-opus-4-8", 5, 25, .1},
	{"claude-opus-4-7", 5, 25, .1}, {"claude-opus-4-6", 5, 25, .1}, {"claude-sonnet-5-5", 2, 10, .1},
	{"claude-sonnet-5", 2, 10, .1}, {"claude-sonnet-4-6", 3, 15, .1}, {"claude-haiku-5-5", .1, .5, .1},
	{"claude-haiku-4-5", 1, 5, .1}, {"claude-fable-5-1", 10, 50, .025}, {"claude-mythos-5-1", 10, 50, .025},
	{"claude-fable-5", 10, 50, .1}, {"claude-mythos-5", 10, 50, .1},
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
	usd := (float64(in)*pin + float64(cr)*pin*prices[best].cr + float64(c1)*pin*2 + float64(c5)*pin*1.25 + float64(out)*pout) / 1e6
	return math.Round(usd*1e6) / 1e6, true
}

// repriceIfChanged reprices every stored request when the price table differs from the one the store was priced
// with (kept as a coverage row), so the store never mixes two price tables.
func repriceIfChanged(db *sql.DB) error {
	sha := fmt.Sprintf("%x", sha1.Sum([]byte(fmt.Sprint(prices))))
	var old sql.NullString
	err := db.QueryRow("select sha from coverage where source='prices' and resource='list'").Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if old.String == sha {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query("select request_id, coalesce(model, ''), tok_in, tok_out, tok_cache_read, tok_cache_write, tok_cw_1h," +
		" tok_cw_5m from request")
	if err != nil {
		return err
	}
	costs := map[string]any{}
	for rows.Next() {
		var id, model string
		var in, out, cr, cw, c1, c5 int64
		if err := rows.Scan(&id, &model, &in, &out, &cr, &cw, &c1, &c5); err != nil {
			rows.Close()
			return err
		}
		costs[id] = nil
		if usd, ok := ListCost(model, in, cr, cw, c1, c5, out); ok {
			costs[id] = usd
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for id, c := range costs {
		if _, err := tx.Exec("update request set cost_list_usd=? where request_id=?", c, id); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("insert into coverage (source, resource, sha, last_ok) values ('prices', 'list', ?, ?)"+
		" on conflict (source, resource) do update set sha=excluded.sha, last_ok=excluded.last_ok", sha,
		utcNow()); err != nil {
		return err
	}
	return tx.Commit()
}
