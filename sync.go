package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

type stateRec struct {
	Plain  string `json:"plain"`
	Cipher string `json:"cipher"`
	At     string `json:"at"`
}

type entry struct {
	Project, Rel, Category, Local, Store string
	Managed                              bool // in the manifest (or already in the store)
	LocalExists, StoreExists             bool
	State                                *stateRec
	Git                                  string
}

type row struct{ project, path, status, git, result string }

// projectReport collects per-project facts printed after the table.
type projectReport struct {
	hiddenTracked int
	noManifest    bool
}

// Google Drive names conflicting copies like ".env (1).age".
var reConflictCopy = regexp.MustCompile(` \(\d+\)(\.[^\\/]*)?$`)

func (a *app) sync() int {
	o := a.o
	if o.command == "push" && len(o.targets) == 0 {
		return fail(errors.New(`push needs a target: PROJECT or PROJECT/PATH`))
	}
	projects, err := a.projectNames()
	if err != nil {
		return fail(err)
	}
	var rows []row
	reports := map[string]projectReport{}
	problems := 0
	for _, p := range projects {
		entries, rep, err := a.entries(p)
		if err != nil {
			return fail(err)
		}
		reports[p] = rep
		for _, e := range entries {
			st, err := a.status(e)
			result := ""
			if err != nil {
				st, result = "error", "ERROR: "+err.Error()
			} else {
				result = a.act(e, st)
			}
			if strings.HasPrefix(result, "REFUSED") || strings.HasPrefix(result, "SKIPPED") || strings.HasPrefix(result, "ERROR") ||
				(o.command == "status" && st == "conflict") {
				problems++
			}
			rows = append(rows, row{p, e.Rel, st, gitNote(e.Git), result})
		}
	}
	a.print(rows, projects, reports)
	if problems > 0 {
		return 2
	}
	return 0
}

func gitNote(g string) string {
	switch g {
	case "untracked", "TRACKED":
		return "NOT GITIGNORED (" + g + ")"
	case "no-git":
		return "no git"
	}
	return ""
}

func (a *app) print(rows []row, projects []string, reports map[string]projectReport) {
	if len(rows) == 0 {
		fmt.Println("Nothing found.")
	} else {
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "Project\tPath\tStatus\tGit\tResult")
		fmt.Fprintln(tw, "-------\t----\t------\t---\t------")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.project, r.path, r.status, r.git, r.result)
		}
		tw.Flush()
	}
	hidden := 0
	var noManifest []string
	for _, p := range projects {
		hidden += reports[p].hiddenTracked
		if reports[p].noManifest && slices.ContainsFunc(rows, func(r row) bool { return r.project == p }) {
			noManifest = append(noManifest, p)
		}
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.status]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, counts[k])
	}
	fmt.Printf("\n[%s] %s\n", a.cfg.Machine, strings.Join(parts, "  "))
	if hidden > 0 {
		fmt.Printf("%d candidate file(s) already tracked by git are hidden (git syncs them; -show-tracked lists them).\n", hidden)
	}
	if len(noManifest) > 0 {
		fmt.Printf("No manifest yet (not synced): %s. Decide with: secret-sync include|exclude PROJECT/PATTERN\n", strings.Join(noManifest, ", "))
	}
}

// ---- selection -------------------------------------------------------------

func splitTarget(t string) (project, path string) {
	parts := strings.SplitN(strings.ReplaceAll(t, `\`, "/"), "/", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return parts[0], ""
}

func (a *app) excluded(name string) bool {
	for _, x := range a.cfg.Exclude {
		if wildcardMatch(x, name) {
			return true
		}
	}
	return false
}

// projectNames: explicit targets; otherwise every project in the store, plus with
// -discover every project folder under projectsRoot.
func (a *app) projectNames() ([]string, error) {
	set := map[string]bool{}
	if len(a.o.targets) > 0 {
		for _, t := range a.o.targets {
			p, _ := splitTarget(t)
			set[p] = true
		}
	} else {
		if a.o.discover {
			des, err := os.ReadDir(a.cfg.ProjectsRoot)
			if err != nil {
				return nil, err
			}
			for _, d := range des {
				if d.IsDir() && !strings.HasPrefix(d.Name(), ".") && !a.excluded(d.Name()) {
					set[d.Name()] = true
				}
			}
		}
		des, _ := os.ReadDir(a.cfg.StoreRoot)
		for _, d := range des {
			if d.IsDir() {
				set[d.Name()] = true
			}
		}
	}
	names := make([]string, 0, len(set))
	for n := range set {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// fileFilter returns the single files requested for a project (slash form), if any.
func (a *app) fileFilter(project string) []string {
	var f []string
	for _, t := range a.o.targets {
		if p, path := splitTarget(t); p == project && path != "" {
			f = append(f, normPattern(path))
		}
	}
	return f
}

// entries builds one entry per relative path that is managed (included in the
// manifest, or already in the store) or is an unmanaged candidate.
func (a *app) entries(project string) ([]*entry, projectReport, error) {
	var rep projectReport
	projDir := filepath.Join(a.cfg.ProjectsRoot, project)
	storeDir := filepath.Join(a.cfg.StoreRoot, project)
	state, err := a.loadState(project)
	if err != nil {
		return nil, rep, err
	}
	m, hasManifest, err := a.loadManifest(project)
	if err != nil {
		return nil, rep, err
	}
	rep.noManifest = !hasManifest

	type info struct {
		cat     string
		managed bool
	}
	files := map[string]*info{}
	if isDir(projDir) {
		for _, f := range walkFiles(projDir) {
			slash := filepath.ToSlash(f.rel)
			name := filepath.Base(f.rel)
			if matchAny(m.Exclude, slash) {
				continue
			}
			if matchAny(m.Include, slash) {
				cat := category(name, f.inSecret)
				if cat == "" {
					cat = "included"
				}
				files[f.rel] = &info{cat: cat, managed: true}
				continue
			}
			if isCandidate(name, f.inSecret) {
				cat := category(name, f.inSecret)
				if cat == "example" {
					continue
				}
				if cat == "" {
					cat = "secrets-folder-other"
				}
				files[f.rel] = &info{cat: cat}
			}
		}
	}
	if isDir(storeDir) {
		_ = filepath.WalkDir(storeDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".age") {
				return nil
			}
			rel, err := filepath.Rel(storeDir, p)
			if err != nil {
				return nil
			}
			rel = strings.TrimSuffix(rel, ".age")
			if reConflictCopy.MatchString(rel) {
				fmt.Printf("  ! %s.age is a sync-service conflict copy. Resolve by hand, then delete it.\n", filepath.Join(project, rel))
				return nil
			}
			if matchAny(m.Exclude, filepath.ToSlash(rel)) {
				return nil
			}
			if f, ok := files[rel]; ok {
				f.managed = true
			} else {
				files[rel] = &info{cat: "stored", managed: true}
			}
			return nil
		})
	}

	filter := a.fileFilter(project)
	var out []*entry
	for rel, f := range files {
		if len(filter) > 0 && !slices.Contains(filter, filepath.ToSlash(rel)) {
			continue
		}
		e := &entry{Project: project, Rel: rel, Category: f.cat, Managed: f.managed,
			Local: filepath.Join(projDir, rel), Store: filepath.Join(storeDir, rel) + ".age"}
		e.LocalExists, e.StoreExists = isFile(e.Local), isFile(e.Store)
		if s, ok := state[filepath.ToSlash(rel)]; ok {
			s := s
			e.State = &s
		}
		out = append(out, e)
	}
	var locals []string
	for _, e := range out {
		if e.LocalExists {
			locals = append(locals, e.Rel)
		}
	}
	if len(locals) > 0 {
		gm := gitStatusMap(projDir, locals)
		for _, e := range out {
			if e.LocalExists {
				e.Git = gm[e.Rel]
			}
		}
	}
	// Candidates already committed to git are git's business: hide them unless asked.
	if !a.o.showTracked {
		kept := out[:0]
		for _, e := range out {
			if !e.Managed && e.Git == "TRACKED" {
				rep.hiddenTracked++
				continue
			}
			kept = append(kept, e)
		}
		out = kept
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, rep, nil
}

// ---- status ----------------------------------------------------------------

func (a *app) status(e *entry) (string, error) {
	if !e.Managed {
		return "candidate", nil
	}
	s := e.State
	switch {
	case e.LocalExists && !e.StoreExists:
		if s != nil {
			return "missing-in-store", nil
		}
		return "new", nil
	case !e.LocalExists && e.StoreExists:
		if s != nil {
			return "missing-locally", nil
		}
		return "remote-new", nil
	case !e.LocalExists:
		return "gone", nil
	}
	if s == nil {
		return a.sameOrConflict(e)
	}
	lh, err := sha256File(e.Local)
	if err != nil {
		return "", err
	}
	ch, err := sha256File(e.Store)
	if err != nil {
		return "", err
	}
	lc, rc := !strings.EqualFold(lh, s.Plain), !strings.EqualFold(ch, s.Cipher)
	switch {
	case !lc && !rc:
		return "in-sync", nil
	case lc && !rc:
		return "local-changed", nil
	case rc && !lc:
		return "remote-changed", nil
	}
	return a.sameOrConflict(e) // both changed: maybe to the same content
}

func (a *app) sameOrConflict(e *entry) (string, error) {
	dh, err := a.decryptedHash(e.Store)
	if err != nil {
		return "", err
	}
	lh, err := sha256File(e.Local)
	if err != nil {
		return "", err
	}
	if dh == lh {
		return "in-sync*", nil
	}
	return "conflict", nil
}

// ---- actions ---------------------------------------------------------------

func (a *app) act(e *entry, st string) string {
	force := a.o.force
	explicit := slices.Contains(a.fileFilter(e.Project), filepath.ToSlash(e.Rel))
	switch a.o.command {
	case "push":
		switch {
		case st == "candidate":
			if explicit {
				return fmt.Sprintf("SKIPPED: not in the manifest; run: secret-sync include %s/%s", e.Project, filepath.ToSlash(e.Rel))
			}
		case st == "new" || st == "local-changed" || st == "missing-in-store" || (st == "conflict" && force):
			return a.push(e)
		case st == "in-sync*":
			return a.link(e)
		case st == "conflict" || st == "remote-changed":
			return "SKIPPED: changed on the other machine; pull first (or -force after deciding)"
		}
	case "pull":
		switch {
		case st == "remote-new" || st == "remote-changed" || st == "missing-locally":
			return a.pull(e, false)
		case st == "conflict" && force:
			return a.pull(e, true)
		case st == "in-sync*":
			return a.link(e)
		case st == "conflict" || st == "local-changed":
			return "SKIPPED: changed locally; push first (or -force to overwrite, local copy is backed up)"
		}
	}
	return ""
}

func (a *app) push(e *entry) string {
	if e.Git == "untracked" || e.Git == "TRACKED" {
		return "REFUSED: not gitignored (" + e.Git + "); fix .gitignore first"
	}
	if a.o.dryRun {
		return "would push"
	}
	plain, err := sha256File(e.Local)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	if err := os.MkdirAll(filepath.Dir(e.Store), 0o700); err != nil {
		return "ERROR: " + err.Error()
	}
	tmp := e.Store + ".tmp"
	defer os.Remove(tmp)
	if err := a.encryptFile(e.Local, tmp); err != nil {
		return "ERROR: " + err.Error()
	}
	if err := os.Rename(tmp, e.Store); err != nil {
		return "ERROR: " + err.Error()
	}
	_ = writeJSON(e.Store+".meta.json", map[string]string{"pushedBy": a.cfg.Machine, "pushedAt": time.Now().Format(time.RFC3339)})
	cipher, err := sha256File(e.Store)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	if err := a.saveState(e.Project, e.Rel, plain, cipher); err != nil {
		return "ERROR: " + err.Error()
	}
	return "pushed"
}

func (a *app) pull(e *entry, backup bool) string {
	if a.o.dryRun {
		return "would pull"
	}
	dir := filepath.Dir(e.Local)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "ERROR: " + err.Error()
	}
	// Decrypt next to the target so the final rename is atomic (same volume).
	tmp := filepath.Join(dir, "."+filepath.Base(e.Local)+".secret-sync-tmp")
	defer os.Remove(tmp)
	if err := a.decryptFile(e.Store, tmp); err != nil {
		return "ERROR: " + err.Error()
	}
	if backup && e.LocalExists {
		bk := filepath.Join(a.cfg.StateDir, "backup", e.Project, e.Rel+"."+time.Now().Format("20060102-150405"))
		if err := copyFile(e.Local, bk); err != nil {
			return "ERROR: backup failed, nothing changed: " + err.Error()
		}
	}
	if err := os.Rename(tmp, e.Local); err != nil {
		return "ERROR: " + err.Error()
	}
	plain, err1 := sha256File(e.Local)
	cipher, err2 := sha256File(e.Store)
	if err := errors.Join(err1, err2); err != nil {
		return "ERROR: " + err.Error()
	}
	if err := a.saveState(e.Project, e.Rel, plain, cipher); err != nil {
		return "ERROR: " + err.Error()
	}
	if backup {
		return "pulled (local copy backed up)"
	}
	return "pulled"
}

func (a *app) link(e *entry) string {
	if a.o.dryRun {
		return "would link"
	}
	plain, err1 := sha256File(e.Local)
	cipher, err2 := sha256File(e.Store)
	if err := errors.Join(err1, err2); err != nil {
		return "ERROR: " + err.Error()
	}
	if err := a.saveState(e.Project, e.Rel, plain, cipher); err != nil {
		return "ERROR: " + err.Error()
	}
	return "linked (already identical)"
}

// ---- local state -----------------------------------------------------------

func (a *app) statePath(project string) string {
	return filepath.Join(a.cfg.StateDir, project+".json")
}

func (a *app) loadState(project string) (map[string]stateRec, error) {
	m := map[string]stateRec{}
	if err := readJSON(a.statePath(project), &m); err != nil {
		return nil, fmt.Errorf("reading state for %s: %w", project, err)
	}
	return m, nil
}

func (a *app) saveState(project, rel, plain, cipher string) error {
	m, err := a.loadState(project)
	if err != nil {
		return err
	}
	m[filepath.ToSlash(rel)] = stateRec{Plain: plain, Cipher: cipher, At: time.Now().Format(time.RFC3339)}
	return writeJSON(a.statePath(project), m)
}
