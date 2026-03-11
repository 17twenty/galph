package backlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEmpty(t *testing.T) {
	items, lines, err := Parse("/nonexistent/BACKLOG.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if items != nil {
		t.Fatalf("expected nil items, got %d", len(items))
	}
	if lines != nil {
		t.Fatalf("expected nil lines, got %d", len(lines))
	}
}

func TestParseMixed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BACKLOG.md")

	content := `# Backlog

- [ ] Fix the flicker
- [x] Fix crash on empty input (refine-001, iteration 52)
- [ ] Add retry logic

Some other text here.
`
	os.WriteFile(path, []byte(content), 0o644)

	items, lines, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if len(lines) != 8 {
		t.Fatalf("expected 8 lines, got %d", len(lines))
	}

	// Check pending
	if items[0].Done || items[0].Description != "Fix the flicker" {
		t.Errorf("item 0: got done=%v desc=%q", items[0].Done, items[0].Description)
	}
	// Check completed
	if !items[1].Done {
		t.Errorf("item 1: expected done")
	}
	// Check second pending
	if items[2].Done || items[2].Description != "Add retry logic" {
		t.Errorf("item 2: got done=%v desc=%q", items[2].Done, items[2].Description)
	}
}

func TestPendingItems(t *testing.T) {
	items := []Item{
		{Description: "a", Done: false},
		{Description: "b", Done: true},
		{Description: "c", Done: false},
	}
	pending := PendingItems(items)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}
	if pending[0].Description != "a" || pending[1].Description != "c" {
		t.Errorf("wrong pending items")
	}
}

func TestMarkDone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BACKLOG.md")

	content := `# Backlog

- [ ] Fix the flicker
- [ ] Add retry logic
`
	os.WriteFile(path, []byte(content), 0o644)

	_, lines, _ := Parse(path)

	err := MarkDone(path, lines, 2, "refine-001", 22)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "- [x] Fix the flicker (refine-001, iteration 22)") {
		t.Errorf("expected marked done, got:\n%s", string(data))
	}
	// Second item should be unchanged
	if !strings.Contains(string(data), "- [ ] Add retry logic") {
		t.Errorf("second item should be unchanged")
	}
}

func TestAppendItem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BACKLOG.md")

	// Should create the file
	err := AppendItem(path, "Fix something", true, "refine-001", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	s := string(data)
	if !strings.Contains(s, "# Backlog") {
		t.Error("expected header")
	}
	if !strings.Contains(s, "- [x] Fix something (refine-001, iteration 5)") {
		t.Errorf("expected completed item, got:\n%s", s)
	}

	// Append a pending item
	AppendItem(path, "Another thing", false, "", 0)
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "- [ ] Another thing") {
		t.Error("expected pending item")
	}
}

func TestEnsureExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "BACKLOG.md")

	err := EnsureExists(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Backlog") {
		t.Error("expected header")
	}

	// Should not overwrite
	os.WriteFile(path, []byte("custom"), 0o644)
	EnsureExists(path)
	data, _ = os.ReadFile(path)
	if string(data) != "custom" {
		t.Error("EnsureExists should not overwrite existing file")
	}
}
