package main

// Detection rules: which files count as sensitive, judged by NAME and PATH only.
// Files are never opened here. Kept in step with scripts/lib/Rules.ps1 (audit).

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func lowerSet(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[strings.ToLower(s)] = true
	}
	return m
}

// Folders never descended into (dependencies, build output, VCS, IDE, and Claude
// Code's own folder: its settings and worktrees are not project secrets).
var pruneDirs = lowerSet("node_modules", ".git", "bin", "obj", "dist", "build", "out", ".venv", "venv", "env",
	"__pycache__", "vendor", ".idea", ".vs", ".vscode", ".next", ".nuxt", "target", "packages",
	".gradle", "coverage", ".cache", ".terraform", ".angular", ".svelte-kit", "Pods", "DerivedData", ".claude")

// Well-known public files that match secret-looking names but are never secrets.
var publicFiles = lowerSet("cacert.pem", "ca-bundle.crt", "ca-certificates.crt")

// Folder names whose files are all considered secrets.
var secretDirs = lowerSet(".secrets", "secrets", ".secret", "secret", "credentials", ".credentials", "certs", ".certs", "keys", ".keys")

var (
	wholeFileExt = lowerSet(".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".der", ".crt", ".cer", ".p8", ".gpg", ".asc", ".kdbx", ".mobileprovision")
	secretKVExt  = lowerSet("", ".txt", ".secret", ".token", ".pwd", ".pass")
	configNames  = lowerSet("local.settings.json", "secrets.json", "usersecrets.json", "config.yaml", "config.yml",
		"secrets.yaml", "secrets.yml", "secrets.toml", ".npmrc", ".pypirc", ".netrc", ".htpasswd", "wp-config.php",
		"settings.local.json", "application-local.properties", "application-local.yml", "docker-compose.override.yml",
		"terraform.tfvars", ".dockercfg", "config.json")

	reExampleEnv  = regexp.MustCompile(`^\.env\.(example|sample|template|dist|defaults)$`)
	reExampleAny  = regexp.MustCompile(`\.(example|sample|template)$`)
	reSSHKey      = regexp.MustCompile(`^id_(rsa|dsa|ecdsa|ed25519)(_sk)?$`)
	reCredJSON    = regexp.MustCompile(`(service[-_]?account|credentials?|client[-_]?secret|firebase[-_]?adminsdk|-key)\S*\.json$`)
	reAppsettings = regexp.MustCompile(`^appsettings(\..+)?\.json$`)
	reAppProps    = regexp.MustCompile(`^application-.*\.(properties|ya?ml)$`)
)

// category guesses what kind of secret a file is from its name, or "" if it is not one.
func category(name string, inSecretDir bool) string {
	n := strings.ToLower(name)
	ext := strings.ToLower(filepath.Ext(n))
	switch {
	case publicFiles[n]:
		return ""
	case reExampleEnv.MatchString(n) || reExampleAny.MatchString(n):
		return "example"
	case reSSHKey.MatchString(n) || ext == ".ppk":
		return "ssh-key"
	case wholeFileExt[ext], reCredJSON.MatchString(n), n == "google-services.json", n == "googleservice-info.plist":
		return "whole-file"
	case n == ".env" || strings.HasPrefix(n, ".env.") || ext == ".env" || n == ".envrc" || n == ".dev.vars":
		return "env-kv"
	case inSecretDir && secretKVExt[ext]:
		return "secrets-folder-kv"
	case inSecretDir:
		return "secrets-folder-other"
	case reAppsettings.MatchString(n) || configNames[n] || ext == ".tfvars" || reAppProps.MatchString(n):
		return "templatable-config"
	}
	return ""
}

func isCandidate(name string, inSecretDir bool) bool {
	if n := strings.ToLower(name); strings.HasSuffix(n, ".pub") || publicFiles[n] {
		return false
	}
	return inSecretDir || category(name, false) != ""
}

type projectFile struct {
	rel      string // relative to the project root, OS separators
	inSecret bool   // inside a folder such as .secrets/
}

// walkFiles lists the regular files of a project, skipping dependency, build,
// VCS and IDE folders. Names only; nothing is opened.
func walkFiles(projectDir string) []projectFile {
	var out []projectFile
	_ = filepath.WalkDir(projectDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if p == projectDir {
			return nil
		}
		if d.IsDir() {
			if pruneDirs[strings.ToLower(d.Name())] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() { // symlinks, devices...
			return nil
		}
		rel, err := filepath.Rel(projectDir, p)
		if err != nil {
			return nil
		}
		inSecret := false
		for _, part := range strings.Split(filepath.Dir(rel), string(os.PathSeparator)) {
			if secretDirs[strings.ToLower(part)] {
				inSecret = true
				break
			}
		}
		out = append(out, projectFile{rel: rel, inSecret: inSecret})
		return nil
	})
	return out
}

// ---- wildcards (case-insensitive; * and ? match across folders, like PowerShell -like) ----

var wildcardCache = map[string]*regexp.Regexp{}

func wildcardMatch(pattern, s string) bool {
	re, ok := wildcardCache[pattern]
	if !ok {
		var b strings.Builder
		b.WriteString("(?is)^")
		for _, r := range pattern {
			switch r {
			case '*':
				b.WriteString(".*")
			case '?':
				b.WriteString(".")
			default:
				b.WriteString(regexp.QuoteMeta(string(r)))
			}
		}
		b.WriteString("$")
		re = regexp.MustCompile(b.String())
		wildcardCache[pattern] = re
	}
	return re.MatchString(s)
}
