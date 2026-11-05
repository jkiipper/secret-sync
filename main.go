// secret-sync keeps the gitignored secret files of each project in sync between
// machines through an age-encrypted folder (for example a Google Drive folder).
//
// Store layout:
//
//	<storeRoot>/<project>/<relative path>.age            encrypted file
//	<storeRoot>/<project>/<relative path>.age.meta.json  who/when pushed
//	<storeRoot>/recipients.txt                           age public keys
//	<storeRoot>/ignore.txt                               <project>\<path> wildcards never synced
//
// Local state (per machine, not synced): <stateDir>/<project>.json holds hashes of
// the last synced plaintext and ciphertext, to tell "changed here" from
// "changed on the other machine".
//
// Safety: never prints file contents (data flows file -> age -> file only), never
// deletes, never overwrites a side that changed since the last sync unless -force
// (a forced pull backs up the local file first), refuses to push files that git
// does not ignore.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"filippo.io/age"
)

const usage = `secret-sync: sync gitignored secret files between machines via an age-encrypted folder.

Usage:
  secret-sync init   -machine NAME -projects-root DIR -store-root DIR -identity FILE [-state-dir DIR]
  secret-sync status [PROJECT | PROJECT/PATH ...] [-discover]
  secret-sync push   PROJECT | PROJECT/PATH ...    [-dry-run] [-force]
  secret-sync pull   [PROJECT | PROJECT/PATH ...]  [-dry-run] [-force]
  secret-sync ignore PROJECT/PATH-WILDCARD ...

Flags:
  -config FILE  config file (default ~/.config/secret-sync/config.json)
  -discover     status: scan every project under projects-root for new candidate files
  -dry-run      show what would happen, change nothing
  -force        overwrite on conflict, after you decided which side wins

Statuses:
  new, local-changed, missing-in-store        -> push
  remote-new, remote-changed, missing-locally -> pull
  in-sync, in-sync* (identical but not linked yet), conflict (changed on both sides), ignored

Exit codes: 0 ok, 1 error, 2 conflicts or refused/skipped files.
`

type config struct {
	Machine      string   `json:"machine"`
	ProjectsRoot string   `json:"projectsRoot"`
	StoreRoot    string   `json:"storeRoot"`
	IdentityFile string   `json:"identityFile"`
	StateDir     string   `json:"stateDir"`
	Exclude      []string `json:"exclude"`
}

// defaultExclude lists project folders that "status -discover" skips. Empty by default;
// each machine can set "exclude" in its config file.
var defaultExclude = []string{}

type options struct {
	command, config                                      string
	targets                                              []string
	discover, force, dryRun, help                        bool
	machine, projectsRoot, storeRoot, identity, stateDir string
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	o, err := parseArgs(args)
	if err != nil {
		return fail(err)
	}
	switch o.command {
	case "", "help":
		fmt.Print(usage)
		return 0
	case "init":
		if err := cmdInit(o); err != nil {
			return fail(err)
		}
		return 0
	case "status", "push", "pull", "ignore":
	default:
		return fail(fmt.Errorf("unknown command %q (try: secret-sync help)", o.command))
	}
	if o.help {
		fmt.Print(usage)
		return 0
	}
	a, err := newApp(o)
	if err != nil {
		return fail(err)
	}
	if o.command == "ignore" {
		if err := a.addIgnores(o.targets); err != nil {
			return fail(err)
		}
		return 0
	}
	return a.sync()
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	return 1
}

// parseArgs accepts flags anywhere, with one or two dashes, any case, and with or
// without separators: -dry-run, --dryRun, -DryRun, -projects-root, -ProjectsRoot ...
func parseArgs(args []string) (*options, error) {
	o := &options{}
	home, _ := os.UserHomeDir()
	o.config = filepath.Join(home, ".config", "secret-sync", "config.json")
	var pos []string
	i := 0
	next := func(flag string, inline string, hasInline bool) (string, error) {
		if hasInline {
			return inline, nil
		}
		if i+1 >= len(args) {
			return "", fmt.Errorf("flag %s needs a value", flag)
		}
		i++
		return args[i], nil
	}
	for ; i < len(args); i++ {
		a := args[i]
		if len(a) < 2 || a[0] != '-' {
			pos = append(pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		name = strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(name))
		var err error
		switch name {
		case "config":
			o.config, err = next(a, val, hasVal)
		case "machine":
			o.machine, err = next(a, val, hasVal)
		case "projectsroot":
			o.projectsRoot, err = next(a, val, hasVal)
		case "storeroot":
			o.storeRoot, err = next(a, val, hasVal)
		case "identity", "identityfile":
			o.identity, err = next(a, val, hasVal)
		case "statedir":
			o.stateDir, err = next(a, val, hasVal)
		case "discover":
			o.discover = true
		case "force":
			o.force = true
		case "dryrun":
			o.dryRun = true
		case "h", "help":
			o.help = true
		default:
			return nil, fmt.Errorf("unknown flag %s", a)
		}
		if err != nil {
			return nil, err
		}
	}
	if len(pos) > 0 {
		o.command = strings.ToLower(pos[0])
		o.targets = pos[1:]
	}
	return o, nil
}

func defaultStateDir() string {
	home, _ := os.UserHomeDir()
	var base string
	switch {
	case runtime.GOOS == "windows" && os.Getenv("LOCALAPPDATA") != "":
		base = os.Getenv("LOCALAPPDATA")
	case os.Getenv("XDG_STATE_HOME") != "":
		base = os.Getenv("XDG_STATE_HOME")
	case runtime.GOOS == "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	default:
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "secret-sync", "state")
}

func cmdInit(o *options) error {
	if o.machine == "" || o.projectsRoot == "" || o.storeRoot == "" || o.identity == "" {
		return errors.New("init needs -machine, -projects-root, -store-root and -identity")
	}
	if o.stateDir == "" {
		o.stateDir = defaultStateDir()
	}
	id, err := loadX25519Identity(o.identity)
	if err != nil {
		return err
	}
	pub := id.Recipient().String() // public key only
	if err := os.MkdirAll(o.storeRoot, 0o700); err != nil {
		return err
	}
	rec := filepath.Join(o.storeRoot, "recipients.txt")
	if data, err := os.ReadFile(rec); err == nil {
		if !strings.Contains(string(data), pub) {
			return fmt.Errorf("this key (%s) is not in %s; use the same key on every machine (restore it from your backup)", pub, rec)
		}
		fmt.Println("recipients.txt already lists this key.")
	} else {
		if err := os.WriteFile(rec, []byte("# age recipients for secret-sync (public keys only)\n"+pub+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Println("Created recipients.txt with public key", pub)
	}
	ign := filepath.Join(o.storeRoot, "ignore.txt")
	if !isFile(ign) {
		if err := os.WriteFile(ign, []byte("# <project>\\<relative path> wildcards that secret-sync never syncs\n"), 0o644); err != nil {
			return err
		}
	}
	cfg := config{Machine: o.machine, ProjectsRoot: o.projectsRoot, StoreRoot: o.storeRoot,
		IdentityFile: o.identity, StateDir: o.stateDir, Exclude: defaultExclude}
	if err := writeJSON(o.config, cfg); err != nil {
		return err
	}
	fmt.Printf("Wrote config %s (machine=%s)\n", o.config, o.machine)
	return nil
}

type app struct {
	o       *options
	cfg     config
	ids     []age.Identity
	recips  []age.Recipient
	ignores []string
}

func newApp(o *options) (*app, error) {
	a := &app{o: o}
	if !isFile(o.config) {
		return nil, fmt.Errorf("no config at %s; run: secret-sync init -machine NAME -projects-root DIR -store-root DIR -identity FILE", o.config)
	}
	if err := readJSON(o.config, &a.cfg); err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	c := a.cfg
	if c.Machine == "" || c.ProjectsRoot == "" || c.StoreRoot == "" || c.IdentityFile == "" || c.StateDir == "" {
		return nil, errors.New("config is incomplete; run init again")
	}
	if a.cfg.Exclude == nil {
		a.cfg.Exclude = defaultExclude
	}
	if !isDir(c.StoreRoot) {
		return nil, fmt.Errorf("store folder not found: %s (is Google Drive running?)", c.StoreRoot)
	}
	var err error
	if a.ids, err = loadIdentities(c.IdentityFile); err != nil {
		return nil, err
	}
	rf, err := os.Open(filepath.Join(c.StoreRoot, "recipients.txt"))
	if err != nil {
		return nil, errors.New("recipients.txt missing in the store; run init")
	}
	defer rf.Close()
	if a.recips, err = age.ParseRecipients(rf); err != nil {
		return nil, fmt.Errorf("recipients.txt: %w", err)
	}
	a.ignores = loadIgnore(filepath.Join(c.StoreRoot, "ignore.txt"))
	return a, nil
}

func (a *app) addIgnores(targets []string) error {
	if len(targets) == 0 {
		return errors.New("ignore needs PROJECT/PATH (wildcards allowed)")
	}
	f, err := os.OpenFile(filepath.Join(a.cfg.StoreRoot, "ignore.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, t := range targets {
		p := strings.ReplaceAll(t, "/", `\`)
		if _, err := fmt.Fprintln(f, p); err != nil {
			return err
		}
		fmt.Println("ignoring", p)
	}
	return nil
}
