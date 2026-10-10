// SPDX-License-Identifier: Apache-2.0

package dataset

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// The real-world dataset: moby/moby at a pinned commit. Selected for a
// substantial tree (13k+ files) with 100 nested .gitignore files spread
// across depths 0-7 (measured 2026-10-09 via the GitHub trees API).
const (
	MobyRepo   = "moby/moby"
	MobyCommit = "9fabd6dfbb926381278c436a866bc8d6ac222274"
	mobyURL    = "https://codeload.github.com/moby/moby/tar.gz/" + MobyCommit
)

// PrepareMoby extracts the pinned checkout into dst, skipping work when the
// marker already matches. Files that cannot be created on the host platform
// (for example reserved Windows names) are counted and skipped.
func PrepareMoby(dst string) (skipped int, err error) {
	marker := filepath.Join(dst, ".bench-moby")
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == MobyCommit {
		return 0, nil
	}
	if err := os.RemoveAll(dst); err != nil {
		return 0, err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	req, err := http.NewRequest(http.MethodGet, mobyURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "go-ignore-benchsetup")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", mobyURL, err)
	}
	defer func() { _ = resp.Body.Close() }() // Read errors are handled below.
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download %s: status %d", mobyURL, resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return 0, err
	}
	defer func() { _ = gz.Close() }() // Closing a gzip reader does not validate its checksum.
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return skipped, err
		}
		// Strip the leading "<repo>-<sha>/" component.
		at := strings.IndexByte(hdr.Name, '/')
		if at < 0 {
			continue
		}
		name := hdr.Name[at+1:]
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return skipped, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return skipped, err
			}
			if err := writeFile(target, tr, hdr.Size); err != nil {
				skipped++
				continue
			}
		case tar.TypeSymlink:
			// Symlinks are materialized as small regular files so the tree
			// stays platform-portable; ignore semantics do not read them.
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return skipped, err
			}
			if err := os.WriteFile(target, []byte(hdr.Linkname), 0o644); err != nil {
				skipped++
				continue
			}
		}
	}
	if err := os.WriteFile(marker, []byte(MobyCommit+"\n"), 0o644); err != nil {
		return skipped, err
	}
	return skipped, nil
}

func writeFile(target string, r io.Reader, size int64) error {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(f, r, size); err != nil {
		_ = f.Close() // Preserve the copy error.
		return err
	}
	return f.Close()
}

// TreeStats summarizes a materialized tree.
type TreeStats struct {
	Files        int
	Dirs         int
	IgnoreFiles  int
	Rules        int
	DepthHist    map[int]int // .gitignore files by directory depth
	MaxPathDepth int
}

// ScanTree walks root and collects tree statistics.
func ScanTree(root string) (TreeStats, error) {
	var st TreeStats
	st.DepthHist = map[int]int{}
	err := filepath.WalkDir(root, func(full string, d os.DirEntry, err error) error {
		if err != nil {
			// .git internals can be unreadable on some hosts; ignore stats stay valid.
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if strings.HasPrefix(slash, ".git/") || slash == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			st.Dirs++
			return nil
		}
		st.Files++
		if d.Name() == ".gitignore" {
			st.IgnoreFiles++
			depth := strings.Count(slash, "/")
			st.DepthHist[depth]++
			raw, err := os.ReadFile(full)
			if err == nil {
				st.Rules += countRuleLines(strings.Split(string(raw), "\n"))
			}
		}
		if pd := strings.Count(slash, "/") + 1; pd > st.MaxPathDepth {
			st.MaxPathDepth = pd
		}
		return nil
	})
	return st, err
}

// TrackedPaths lists all file paths (slash-relative, excluding .git) in
// deterministic lexicographic order.
func TrackedPaths(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(full string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if d.IsDir() {
			if slash == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		out = append(out, slash)
		return nil
	})
	return out, err
}
