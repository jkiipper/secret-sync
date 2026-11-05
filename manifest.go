package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// manifestName is the per-project manifest kept in the store, next to the encrypted files.
// It holds patterns only, never values.
const manifestName = "secret-sync.json"

type manifest struct {
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

func (a *app) manifestPath(project string) string {
	return filepath.Join(a.cfg.StoreRoot, project, manifestName)
}

// loadManifest returns the project's manifest and whether it exists.
func (a *app) loadManifest(project string) (*manifest, bool, error) {
	m := &manifest{Include: []string{}, Exclude: []string{}}
	p := a.manifestPath(project)
	if !isFile(p) {
		return m, false, nil
	}
	if err := readJSON(p, m); err != nil {
		return nil, false, fmt.Errorf("reading manifest of %s: %w", project, err)
	}
	return m, true, nil
}

// normPattern makes a pattern relative to the project root with "/" separators.
func normPattern(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	p = strings.TrimPrefix(p, "./")
	return strings.Trim(p, "/")
}

func matchAny(patterns []string, slashRel string) bool {
	for _, p := range patterns {
		if wildcardMatch(normPattern(p), slashRel) {
			return true
		}
	}
	return false
}

// editManifest adds patterns to the include or exclude list of each project's manifest.
func (a *app) editManifest(kind string, targets []string) error {
	if len(targets) == 0 {
		return fmt.Errorf("%s needs PROJECT/PATTERN ... (e.g. myapp/.env myapp/.secrets/*)", kind)
	}
	byProject := map[string][]string{}
	var order []string
	for _, t := range targets {
		proj, pat := splitTarget(t)
		pat = normPattern(pat)
		if proj == "" || pat == "" {
			return fmt.Errorf("%q: expected PROJECT/PATTERN", t)
		}
		if !isDir(filepath.Join(a.cfg.ProjectsRoot, proj)) && !isDir(filepath.Join(a.cfg.StoreRoot, proj)) {
			return fmt.Errorf("unknown project %q (not found in %s)", proj, a.cfg.ProjectsRoot)
		}
		if _, ok := byProject[proj]; !ok {
			order = append(order, proj)
		}
		byProject[proj] = append(byProject[proj], pat)
	}
	for _, proj := range order {
		m, existed, err := a.loadManifest(proj)
		if err != nil {
			return err
		}
		list := &m.Include
		if kind == "exclude" {
			list = &m.Exclude
		}
		for _, pat := range byProject[proj] {
			if slices.Contains(*list, pat) {
				fmt.Printf("%s %s/%s (already listed)\n", kind, proj, pat)
				continue
			}
			*list = append(*list, pat)
			fmt.Printf("%s %s/%s\n", kind, proj, pat)
		}
		if a.o.dryRun {
			fmt.Println("(dry run: manifest not saved)")
			continue
		}
		if err := writeJSON(a.manifestPath(proj), m); err != nil {
			return err
		}
		if !existed {
			fmt.Printf("created manifest for %s\n", proj)
		}
	}
	return nil
}
