// Package digest builds the twice-daily goal report (step 5): per lead the GOAL block of its status file
// (RULES 3.1), checked against the bus, plus what needs the owner. It runs with no model.
package digest

import "github.com/orzazade/crius/internal/goal"

// The GOAL block grammar lives in package goal, which wakewatch reads too (an idle lead whose goal is done is not idle).
type (
	State      = goal.State
	Milestone  = goal.Milestone
	Goal       = goal.Goal
	Unreadable = goal.Unreadable
)

const (
	Todo  = goal.Todo
	Doing = goal.Doing
	Done  = goal.Done
)

var (
	ErrLost   = goal.ErrLost
	ReadGoal  = goal.ReadGoal
	ParseGoal = goal.ParseGoal
)
