// Package offhours owns the weekly awake grid, the scheduled actions derived
// from it, and the wake/grace rules. It is pure: no AWS or database access.
package offhours

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const emptyGridMessage = "Paint at least one awake hour. To stop a service for good, set its desired count to 0."

// Week is the 7x24 awake grid: index day*24 + hour, day 0 = Monday.
type Week [168]bool

// ParseWeek parses the 168-character 0/1 database form.
func ParseWeek(s string) (Week, error) {
	var w Week
	if len(s) != len(w) {
		return w, fmt.Errorf("The weekly grid must have 168 hours, got %d.", len(s))
	}
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '0':
		case '1':
			w[i] = true
		default:
			return Week{}, errors.New("The weekly grid may only contain 0 and 1.")
		}
	}
	return w, nil
}

// String returns the 168-character 0/1 database form.
func (w Week) String() string {
	b := make([]byte, len(w))
	for i, awake := range w {
		b[i] = '0'
		if awake {
			b[i] = '1'
		}
	}
	return string(b)
}

// AwakeHours counts awake hours in the week.
func (w Week) AwakeHours() int {
	n := 0
	for _, awake := range w {
		if awake {
			n++
		}
	}
	return n
}

// WeekFromRows builds a Week from the JSON form (rows are days, Monday first).
func WeekFromRows(rows [7][24]bool) Week {
	var w Week
	for d := range rows {
		for h := range rows[d] {
			w[d*24+h] = rows[d][h]
		}
	}
	return w
}

// Rows returns the JSON form of the grid.
func (w Week) Rows() [7][24]bool {
	var rows [7][24]bool
	for i, awake := range w {
		rows[i/24][i%24] = awake
	}
	return rows
}

// ValidateAWSTag checks an AWS tag key/value a schedule selects by. An empty value is allowed.
func ValidateAWSTag(tagKey, tagValue string) error {
	if n := utf8.RuneCountInString(tagKey); n < 1 || n > 128 {
		return errors.New("The tag key must be 1 to 128 characters.")
	}
	if strings.HasPrefix(tagKey, "aws:") {
		return errors.New("The tag key must not start with aws: because AWS reserves that prefix.")
	}
	if utf8.RuneCountInString(tagValue) > 256 {
		return errors.New("The tag value must be at most 256 characters.")
	}
	return nil
}

// Validate checks a schedule's name, timezone and grid and returns its location.
func Validate(name, timezone string, w Week) (*time.Location, error) {
	if n := utf8.RuneCountInString(strings.TrimSpace(name)); n < 1 || n > 80 {
		return nil, errors.New("The schedule name must be 1 to 80 characters.")
	}
	if timezone == "" || timezone == "Local" {
		return nil, errors.New("Pick an IANA timezone such as Europe/Berlin.")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, fmt.Errorf("Unknown timezone %q. Pick an IANA timezone such as Europe/Berlin.", timezone)
	}
	if w.AwakeHours() == 0 {
		return nil, errors.New(emptyGridMessage)
	}
	return loc, nil
}
