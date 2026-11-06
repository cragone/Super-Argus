// Package ruler evaluates alert rules on a schedule and drives their state machine.
package ruler

import (
	"errors"
	"fmt"
	"time"

	"github.com/cragone/Super-Argus/internal/models"
	"github.com/cragone/Super-Argus/internal/query"
)

// Limits on rule evaluation, tighter than interactive queries.
const (
	MaxWindow      = time.Hour
	MaxForDuration = 24 * time.Hour
	MaxCooldown    = 7 * 24 * time.Hour
	MinWindow      = 15 * time.Second
)

// ParseOptDuration parses a duration, treating "", "0" and "0s" as zero.
func ParseOptDuration(s string) (time.Duration, error) {
	switch s {
	case "", "0", "0s", "0m":
		return 0, nil
	}
	return query.ParseDuration(s)
}

// Breached reports whether value violates the comparator/threshold.
func Breached(comparator string, value, threshold float64) bool {
	switch comparator {
	case "gt":
		return value > threshold
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	case "eq":
		return value == threshold
	}
	return false
}

// ValidComparator reports whether c is a known comparator.
func ValidComparator(c string) bool {
	switch c {
	case "gt", "gte", "lt", "lte", "eq":
		return true
	}
	return false
}

// Validate checks a rule's query and durations.
func Validate(r *models.AlertRule) error {
	if !query.IsMetric(r.Query) {
		return errors.New("query must be a metric query such as count_over_time({service=\"api\"}[5m])")
	}
	if _, err := query.ParseMetric(r.Query); err != nil {
		return err
	}
	if !ValidComparator(r.Comparator) {
		return errors.New("comparator must be one of gt, gte, lt, lte, eq")
	}
	w, err := ParseOptDuration(r.Window)
	if err != nil || w < MinWindow || w > MaxWindow {
		return fmt.Errorf("window must be a duration between %s and %s", MinWindow, MaxWindow)
	}
	f, err := ParseOptDuration(r.ForDuration)
	if err != nil || f > MaxForDuration {
		return fmt.Errorf("for_duration must be a duration up to %s", MaxForDuration)
	}
	c, err := ParseOptDuration(r.Cooldown)
	if err != nil || c > MaxCooldown {
		return fmt.Errorf("cooldown must be a duration up to %s", MaxCooldown)
	}
	switch r.Severity {
	case "info", "warning", "critical":
	default:
		return errors.New("severity must be info, warning or critical")
	}
	return nil
}

// Transition is the outcome of one state-machine step.
type Transition struct {
	State   string // new state
	Since   time.Time
	Changed bool
	// Event is "" (none), "pending", "firing" or "resolved".
	Event string
}

// Step advances a rule's state given whether the latest evaluation breached.
// It is pure so it can be tested without a clock or database.
func Step(cur string, since, now time.Time, breached bool, forDur time.Duration) Transition {
	t := Transition{State: cur, Since: since}
	switch cur {
	case models.AlertOK:
		if breached {
			if forDur <= 0 {
				t = Transition{State: models.AlertFiring, Since: now, Changed: true, Event: models.AlertFiring}
			} else {
				t = Transition{State: models.AlertPending, Since: now, Changed: true, Event: models.AlertPending}
			}
		}
	case models.AlertPending:
		switch {
		case !breached:
			t = Transition{State: models.AlertOK, Since: now, Changed: true}
		case now.Sub(since) >= forDur:
			t = Transition{State: models.AlertFiring, Since: now, Changed: true, Event: models.AlertFiring}
		}
	case models.AlertFiring:
		if !breached {
			t = Transition{State: models.AlertOK, Since: now, Changed: true, Event: models.AlertResolved}
		}
	}
	return t
}
