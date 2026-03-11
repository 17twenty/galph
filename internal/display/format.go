package display

import (
	"fmt"
	"time"
)

// FormatDuration formats a duration for display, using compact notation.
func FormatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", m, s)
}

// FormatCost formats a USD cost for display.
func FormatCost(cost float64) string {
	return fmt.Sprintf("$%.4f", cost)
}
