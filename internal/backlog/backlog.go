// Package backlog manages BACKLOG.md — a persistent file tracking
// refinements, bug fixes, and post-PRD work items.
package backlog

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Item represents a single entry in BACKLOG.md.
type Item struct {
	Line        int    // 0-indexed line number in the file
	Description string // text after the checkbox
	Done        bool   // [x] = true, [ ] = false
}

var (
	pendingRe  = regexp.MustCompile(`^\s*- \[ \] (.+)$`)
	completedRe = regexp.MustCompile(`^\s*- \[x\] (.+)$`)
)

// Parse reads a BACKLOG.md file and returns all items plus the raw lines.
// Returns nil items and nil error if the file does not exist.
func Parse(path string) ([]Item, []string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("reading backlog: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	var items []Item

	for i, line := range lines {
		if m := pendingRe.FindStringSubmatch(line); m != nil {
			items = append(items, Item{Line: i, Description: m[1], Done: false})
		} else if m := completedRe.FindStringSubmatch(line); m != nil {
			items = append(items, Item{Line: i, Description: m[1], Done: true})
		}
	}

	return items, lines, nil
}

// PendingItems returns only items where Done == false.
func PendingItems(items []Item) []Item {
	var pending []Item
	for _, item := range items {
		if !item.Done {
			pending = append(pending, item)
		}
	}
	return pending
}

// MarkDone rewrites the file, flipping the item at the given line to [x]
// and appending metadata (task ID, iteration number).
func MarkDone(path string, lines []string, lineNum int, taskID string, iteration int) error {
	if lineNum < 0 || lineNum >= len(lines) {
		return fmt.Errorf("line %d out of range", lineNum)
	}

	line := lines[lineNum]
	m := pendingRe.FindStringSubmatch(line)
	if m == nil {
		return fmt.Errorf("line %d is not a pending item", lineNum)
	}

	// Replace [ ] with [x] and append metadata
	lines[lineNum] = fmt.Sprintf("- [x] %s (%s, iteration %d)", m[1], taskID, iteration)

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

// AppendItem adds a new item to BACKLOG.md. Creates the file if it doesn't exist.
// If done is true, the item is marked as [x]; otherwise [ ].
func AppendItem(path string, description string, done bool, taskID string, iteration int) error {
	if err := EnsureExists(path); err != nil {
		return err
	}

	var line string
	if done {
		line = fmt.Sprintf("- [x] %s (%s, iteration %d)\n", description, taskID, iteration)
	} else {
		line = fmt.Sprintf("- [ ] %s\n", description)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening backlog: %w", err)
	}
	defer f.Close()

	_, err = f.WriteString(line)
	return err
}

// EnsureExists creates BACKLOG.md with a header if it doesn't exist.
func EnsureExists(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}

	header := "# Backlog\n\nRefinements, bug fixes, and post-PRD work items.\n\n"
	return os.WriteFile(path, []byte(header), 0o644)
}

// Exists returns true if the backlog file exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
