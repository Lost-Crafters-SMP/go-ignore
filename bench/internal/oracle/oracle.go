// SPDX-License-Identifier: Apache-2.0

// Package oracle labels benchmark queries with Git's own ignore verdict,
// using the git binary strictly as a black-box behavioural reference.
package oracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"go.lostcrafters.com/ignore/bench/internal/dataset"
)

// HygieneFiles creates the empty global config and XDG directory git should
// see instead of the host user's, and returns the isolated environment.
func HygieneFiles(cache string) ([]string, error) {
	empty := filepath.Join(cache, "empty-config")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		return nil, err
	}
	xdg := filepath.Join(cache, "xdg")
	if err := os.MkdirAll(xdg, 0o755); err != nil {
		return nil, err
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		switch strings.ToUpper(key) {
		case "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "XDG_CONFIG_HOME", "LC_ALL":
			continue
		}
		env = append(env, e)
	}
	env = append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+empty,
		"XDG_CONFIG_HOME="+xdg,
		"LC_ALL=C",
	)
	return env, nil
}

// ListNonIgnored returns every file a purely rule-based traversal of dir
// discovers as not ignored, using Git's own machinery: tracked files plus
// untracked files surviving the exclude stack, minus tracked files that the
// ignore rules themselves exclude. Git keeps tracked files regardless of
// ignore rules, but a rule-matching walker has no index and must judge them
// by the rules, so subtracting `ls-files --cached --ignored` gives the
// discovery semantics a walker can be held to. The .git namespace is
// excluded. It is the whole-tree traversal oracle for file discovery: a
// walker that prunes exactly where Git prunes lands on precisely this set,
// so a wrongly pruned directory shows up as missing files.
func ListNonIgnored(dir, cache string) (map[string]bool, error) {
	env, err := HygieneFiles(cache)
	if err != nil {
		return nil, err
	}
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = env
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git %v: %w stderr=%s", args, err, stderr.String())
		}
		return out, nil
	}
	discovered, err := run("ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	ruleIgnored, err := run("ls-files", "-z", "--cached", "--ignored", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	skip := make(map[string]bool)
	for _, p := range strings.Split(strings.TrimSuffix(string(ruleIgnored), "\x00"), "\x00") {
		if p != "" {
			skip[p] = true
		}
	}
	files := make(map[string]bool)
	for _, p := range strings.Split(strings.TrimSuffix(string(discovered), "\x00"), "\x00") {
		if p == "" || p == ".git" || strings.HasPrefix(p, ".git/") || skip[p] {
			continue
		}
		files[p] = true
	}
	return files, nil
}

// Version reports the git binary version.
func Version() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// Init turns dir into a bare-hygiene git repository without templates so
// check-ignore sees only the tree's own ignore files.
func Init(dir, cache string) error {
	env, err := HygieneFiles(cache)
	if err != nil {
		return err
	}
	empty := filepath.Join(cache, "empty-config")
	run := func(args ...string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
		return nil
	}
	if err := run("init", "--quiet", "--template="); err != nil {
		return err
	}
	if err := run("config", "core.excludesFile", empty); err != nil {
		return err
	}
	// Deep synthetic trees exceed the classic Win32 path limit; without
	// this, git's directory checks silently fail and the oracle mislabels
	// dir-only ancestor blocks on Windows.
	if err := run("config", "core.longpaths", "true"); err != nil {
		return err
	}
	return nil
}

// Label materializes each query path inside dir (files or directories) and
// assigns q.Ignored from `git check-ignore` run over one batch. Materialized
// query paths stay in the tree: they are the untracked artefacts the
// benchmarks traverse.
func Label(dir, cache string, queries []dataset.Query) error {
	for _, q := range queries {
		full := filepath.Join(dir, filepath.FromSlash(q.Path))
		if q.IsDir {
			if err := os.MkdirAll(full, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(full, nil, 0o644); err != nil {
				return err
			}
		}
	}
	var in bytes.Buffer
	for _, q := range queries {
		in.WriteString(q.Path)
		in.WriteByte(0)
	}
	env, err := HygieneFiles(cache)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "check-ignore", "-z", "--stdin")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = &in
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("git check-ignore: %w stderr=%s", err, stderr.String())
		}
	}
	ignored := map[string]bool{}
	for _, p := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	for i := range queries {
		queries[i].Ignored = ignored[queries[i].Path]
	}
	return nil
}
