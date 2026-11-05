package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git")) // folder, or file for worktrees/submodules
	return err == nil
}

// gitStatusMap returns, per relative path: "TRACKED", "ignored", "untracked" or "no-git".
// Read-only git commands only. If git is missing, files count as "untracked",
// so pushes are refused rather than risked.
func gitStatusMap(projectDir string, rels []string) map[string]string {
	m := make(map[string]string, len(rels))
	if !isGitRepo(projectDir) {
		for _, r := range rels {
			m[r] = "no-git"
		}
		return m
	}
	slash := make([]string, len(rels))
	for i, r := range rels {
		slash[i] = filepath.ToSlash(r)
	}
	tracked := lineSet(runGit(projectDir, append([]string{"ls-files", "--cached", "--"}, slash...)))
	ignored := lineSet(runGit(projectDir, append([]string{"check-ignore", "--no-index", "--"}, slash...)))
	for i, r := range rels {
		switch {
		case tracked[slash[i]]:
			m[r] = "TRACKED"
		case ignored[slash[i]]:
			m[r] = "ignored"
		default:
			m[r] = "untracked"
		}
	}
	return m
}

func runGit(dir string, args []string) string {
	cmd := exec.Command("git", append([]string{"-c", "core.quotepath=false", "-C", dir}, args...)...)
	out, _ := cmd.Output() // check-ignore exits 1 when nothing is ignored; the output is what matters
	return string(out)
}

func lineSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(s, "\n") {
		l = strings.Trim(strings.TrimSpace(l), `"`)
		if l != "" {
			m[l] = true
		}
	}
	return m
}
