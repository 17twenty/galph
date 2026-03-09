// Package hasher provides deterministic content hashing for change detection.
// Uses pure Go crypto/sha256 — no shelling out to sha256sum or git.
package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// HashFiles computes a single deterministic SHA-256 hash over the contents
// of the given file paths. Files are sorted by path before hashing.
// Missing files are skipped (not an error).
func HashFiles(paths []string) (string, error) {
	sorted := make([]string, len(paths))
	copy(sorted, paths)
	sort.Strings(sorted)

	h := sha256.New()
	for _, p := range sorted {
		f, err := os.Open(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("opening %s: %w", p, err)
		}
		// Include filename as separator to prevent content collisions
		fmt.Fprintf(h, "\x00%s\x00", p)
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("reading %s: %w", p, err)
		}
		f.Close()
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashPlanInputs computes the hash of files that affect planning:
// PRD, CLAUDE.md, and .galphrc relative to the workspace directory.
func HashPlanInputs(workspace, prdPath string) (string, error) {
	paths := []string{
		prdPath,
		filepath.Join(workspace, "CLAUDE.md"),
		filepath.Join(workspace, ".galphrc"),
	}
	return HashFiles(paths)
}
