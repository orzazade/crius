package playbook

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// flaky fails its first pick, then behaves like the scripted model.
type flaky struct {
	scripted
	fail error
	n    int
}

func (m *flaky) Pick(ctx context.Context, cs []Candidate) ([]Verdict, error) {
	m.n++
	if m.n == 1 {
		return nil, m.fail
	}
	return m.scripted.Pick(ctx, cs)
}

// A bad model reply parks its batch; the next run answers it and nothing is lost.
func TestFailedPickIsRetriedNextRun(t *testing.T) {
	m := &flaky{fail: fmt.Errorf("pick reply is not a JSON array")}
	cfg, _ := dayConfig(t)
	res, err := Run(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Failed) != 1 || res.New != 0 {
		t.Fatalf("first run: failed %v new %d, want one failed batch and nothing written", res.Failed, res.New)
	}
	res, err = Run(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 3 || res.New != 2 || res.Evidence != 1 {
		t.Fatalf("second run: %d candidates, new %d, evidence %d; want the 3 parked ones answered (2 new, 1 evidence)",
			len(res.Candidates), res.New, res.Evidence)
	}
}

// An API-key login stops the run with nothing saved.
func TestAPIKeyStopsTheRun(t *testing.T) {
	m := &flaky{fail: fmt.Errorf("%w (apiKeySource %q)", ErrAPIKey, "ANTHROPIC_API_KEY")}
	cfg, _ := dayConfig(t)
	if _, err := Run(context.Background(), cfg, m); !errors.Is(err, ErrAPIKey) {
		t.Fatalf("err = %v, want ErrAPIKey", err)
	}
	cur, _ := loadCursor(cfg.StateDir)
	if len(cur.Bus) != 0 || len(cur.Pending) != 0 {
		t.Fatalf("cursor saved after an API-key stop: %+v", cur)
	}
}
