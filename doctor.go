package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"filippo.io/age"
)

// version is set at release build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

// defaultStoreName is the folder created inside the sync service's root.
const defaultStoreName = "dev-secrets"

func defaultIdentity() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "secret-sync", "age-key.txt")
}

// ---- keygen ----------------------------------------------------------------

// cmdKeygen creates a new age X25519 key. The private key goes to a file only
// readable by the owner; only the public key is printed.
func cmdKeygen(o *options) error {
	p := o.identity
	if p == "" {
		p = defaultIdentity()
	}
	if isFile(p) {
		return fmt.Errorf("%s already exists; refusing to overwrite a key", p)
	}
	if existing := existingStores(o); len(existing) > 0 && !o.force {
		return fmt.Errorf("a secret-sync store already exists at %s: restore the SAME key your other machines use "+
			"(from your backup) instead of creating a new one. Use -force only if you really want a second key", strings.Join(existing, ", "))
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return err
	}
	pub := id.Recipient().String()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf("# created: %s\n# public key: %s\n%s\n", time.Now().Format(time.RFC3339), pub, id.String())
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		return err
	}
	fmt.Println("Created", p)
	fmt.Println("Public key:", pub)
	fmt.Println("Back this file up now (e.g. in your password manager). Without it the store cannot be decrypted.")
	return nil
}

// existingStores lists stores (folders with a recipients.txt) that keygen must not ignore:
// the -store-root given, or else the configured store and any in detected Drive folders.
func existingStores(o *options) []string {
	var candidates []string
	if o.storeRoot != "" {
		candidates = []string{o.storeRoot}
	} else {
		var cfg config
		if isFile(o.config) && readJSON(o.config, &cfg) == nil && cfg.StoreRoot != "" {
			candidates = append(candidates, cfg.StoreRoot)
		}
		for _, r := range detectDriveRoots() {
			candidates = append(candidates, filepath.Join(r, defaultStoreName))
		}
	}
	var out []string
	for _, c := range candidates {
		if isFile(filepath.Join(c, "recipients.txt")) {
			out = append(out, c)
		}
	}
	return out
}

// ---- doctor ----------------------------------------------------------------

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok, warn, fail, manual
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
	By     string `json:"by,omitempty"` // who runs the fix: "agent" or "user"
}

type doctorReport struct {
	Version  string  `json:"version"`
	Platform string  `json:"platform"`
	Config   string  `json:"config"`
	Checks   []check `json:"checks"`
	Ready    bool    `json:"ready"`
}

// Localized names of the Google Drive root folder.
var driveNames = []string{"My Drive", "Meu Drive", "Mi unidad", "Mon Drive", "Meine Ablage", "Il mio Drive", "Mijn Drive", "Mój dysk"}

// detectDriveRoots finds Google Drive for desktop root folders.
func detectDriveRoots() []string {
	var out []string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		for c := 'C'; c <= 'Z'; c++ {
			for _, n := range driveNames {
				if p := string(c) + `:\` + n; isDir(p) {
					out = append(out, p)
				}
			}
		}
	case "darwin":
		accounts, _ := filepath.Glob(filepath.Join(home, "Library", "CloudStorage", "GoogleDrive-*"))
		for _, acc := range accounts {
			for _, n := range driveNames {
				if p := filepath.Join(acc, n); isDir(p) {
					out = append(out, p)
				}
			}
		}
	default: // Linux: no official client; common rclone mount points
		for _, n := range []string{"GoogleDrive", "gdrive", "Google Drive"} {
			if p := filepath.Join(home, n); isDir(p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// guessProjectsRoot: the parent of the git repository containing the current folder.
func guessProjectsRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for d := dir; ; d = filepath.Dir(d) {
		if isGitRepo(d) {
			return filepath.Dir(d)
		}
		if filepath.Dir(d) == d {
			return filepath.Dir(dir)
		}
	}
}

func installHint(what string) string {
	hints := map[string]map[string]string{
		"git": {
			"windows": "winget install --id Git.Git -e",
			"darwin":  "xcode-select --install   (or: brew install git)",
			"linux":   "sudo apt install git   (or your distribution's package manager)",
		},
		"drive": {
			"windows": "winget install --id Google.GoogleDrive -e   then sign in",
			"darwin":  "brew install --cask google-drive   then sign in",
			"linux":   "no official client: mount Google Drive with rclone (https://rclone.org/drive/) or use another synced folder",
		},
	}
	goos := runtime.GOOS
	if goos != "windows" && goos != "darwin" {
		goos = "linux"
	}
	return hints[what][goos]
}

func q(s string) string { return `"` + s + `"` }

func cmdDoctor(o *options) int {
	rep := doctorReport{Version: version, Platform: runtime.GOOS + "/" + runtime.GOARCH, Config: o.config}
	add := func(c check) { rep.Checks = append(rep.Checks, c) }

	var cfg config
	hasConfig := isFile(o.config) && readJSON(o.config, &cfg) == nil && cfg.StoreRoot != ""

	// 1. git, for the .gitignore safety check.
	if p, err := exec.LookPath("git"); err == nil {
		add(check{Name: "git", Status: "ok", Detail: p})
	} else {
		add(check{Name: "git", Status: "fail", Detail: "git not found; needed to check that files are gitignored",
			Fix: installHint("git"), By: "user"})
	}

	// 2. Sync folder and store.
	storeRoot := o.storeRoot
	if storeRoot == "" && hasConfig {
		storeRoot = cfg.StoreRoot
	}
	if storeRoot == "" {
		roots := detectDriveRoots()
		switch {
		case len(roots) == 0:
			add(check{Name: "sync folder", Status: "fail", Detail: "no Google Drive folder found",
				Fix: installHint("drive"), By: "user"})
		default:
			storeRoot = filepath.Join(roots[0], defaultStoreName)
			detail := "Google Drive at " + roots[0]
			if len(roots) > 1 {
				detail += fmt.Sprintf(" (also found: %s; pass -store-root to choose)", strings.Join(roots[1:], ", "))
			}
			add(check{Name: "sync folder", Status: "ok", Detail: detail})
		}
	} else {
		parent := filepath.Dir(storeRoot)
		if isDir(parent) {
			add(check{Name: "sync folder", Status: "ok", Detail: parent})
		} else {
			add(check{Name: "sync folder", Status: "fail", Detail: parent + " not found (is the sync app running and signed in?)",
				Fix: installHint("drive"), By: "user"})
		}
	}
	recipients := ""
	if storeRoot != "" {
		recPath := filepath.Join(storeRoot, "recipients.txt")
		if data, err := os.ReadFile(recPath); err == nil {
			recipients = string(data)
			n := 0
			if des, err := os.ReadDir(storeRoot); err == nil {
				for _, d := range des {
					if d.IsDir() {
						n++
					}
				}
			}
			add(check{Name: "store", Status: "ok", Detail: fmt.Sprintf("%s (%d project folder(s))", storeRoot, n)})
		} else {
			where := storeRoot + " does not exist"
			if isDir(storeRoot) {
				where = storeRoot + " exists but has no recipients.txt"
			}
			add(check{Name: "store", Status: "warn", Detail: where + ". DECIDE: if you already use secret-sync on another machine, " +
				"wait until the sync app has finished downloading it and run doctor again (do not create a new key). " +
				"Only if this is your very first machine, create a new store (keygen, then init -new-store)."})
		}
		add(check{Name: "offline access", Status: "manual",
			Detail: "the store must be available offline, or pulls fail without internet",
			Fix:    "in the sync app, mark " + q(storeRoot) + " as available offline (Google Drive: right-click > Offline access > Available offline)", By: "user"})
	}

	// 3. age key.
	identity := o.identity
	if identity == "" && hasConfig {
		identity = cfg.IdentityFile
	}
	if identity == "" {
		identity = defaultIdentity()
	}
	keyOK := false
	if !isFile(identity) {
		if recipients != "" {
			add(check{Name: "age key", Status: "fail", Detail: identity + " missing; the store already exists, so use the SAME key as your other machines",
				Fix: "restore the key from your backup (e.g. password manager) to " + q(identity), By: "user"})
		} else {
			add(check{Name: "age key", Status: "fail", Detail: identity + " missing. No store found yet: restore your key from your backup, " +
				"or, ONLY if this is your very first machine, create one",
				Fix: "first machine only: secret-sync keygen -identity " + q(identity) + "   then back the file up", By: "user"})
		}
	} else if id, err := loadX25519Identity(identity); err != nil {
		add(check{Name: "age key", Status: "fail", Detail: err.Error(), Fix: "check or restore the key file", By: "user"})
	} else {
		pub := id.Recipient().String()
		switch {
		case recipients != "" && !strings.Contains(recipients, pub):
			add(check{Name: "age key", Status: "fail", Detail: "this key (" + pub + ") is not the store's key",
				Fix: "restore the key used by your other machines to " + q(identity), By: "user"})
		default:
			keyOK = true
			add(check{Name: "age key", Status: "ok", Detail: identity + " (public key " + pub + ")"})
		}
		if runtime.GOOS != "windows" {
			if st, err := os.Stat(identity); err == nil && st.Mode().Perm()&0o077 != 0 {
				add(check{Name: "key permissions", Status: "warn", Detail: "key file is readable by other users",
					Fix: "chmod 600 " + q(identity), By: "agent"})
			}
		}
	}

	// 4. Config.
	if hasConfig {
		add(check{Name: "config", Status: "ok", Detail: fmt.Sprintf("%s (machine %s, projects in %s)", o.config, cfg.Machine, cfg.ProjectsRoot)})
	} else {
		host, _ := os.Hostname()
		projects := o.projectsRoot
		if projects == "" {
			projects = guessProjectsRoot()
		}
		fix, by := "", "agent"
		if storeRoot != "" {
			fix = fmt.Sprintf("secret-sync init -machine %s -projects-root %s -store-root %s -identity %s", q(host), q(projects), q(storeRoot), q(identity))
			if recipients == "" {
				// Creating a store is the user's decision (see the store check), never the agent's.
				fix, by = "first machine only: "+fix+" -new-store", "user"
			}
		}
		detail := "no config at " + o.config
		if !keyOK || storeRoot == "" {
			detail += " (fix the items above first)"
		}
		add(check{Name: "config", Status: "fail", Detail: detail, Fix: fix, By: by})
	}

	rep.Ready = true
	for _, c := range rep.Checks {
		if c.Status == "fail" {
			rep.Ready = false
		}
	}

	if o.json {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(rep)
	} else {
		fmt.Printf("secret-sync %s on %s\n\n", rep.Version, rep.Platform)
		for _, c := range rep.Checks {
			fmt.Printf("[%-6s] %-16s %s\n", strings.ToUpper(c.Status), c.Name, c.Detail)
			if c.Fix != "" {
				who := "you"
				if c.By == "agent" {
					who = "agent"
				}
				fmt.Printf("%9s %-16s %s\n", "", "fix ("+who+"):", c.Fix)
			}
		}
		if rep.Ready {
			fmt.Println("\nReady.")
		} else {
			fmt.Println("\nNot ready: fix the FAIL items in order, then run doctor again.")
		}
	}
	if !rep.Ready {
		return 2
	}
	return 0
}
