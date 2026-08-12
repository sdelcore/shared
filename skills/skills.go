// Package skills embeds the agent skills shipped with shared. The files stay
// readable in the repo (and on GitHub) while both binaries carry a copy: the
// CLI writes them out, and the server hands them to any agent over HTTP.
package skills

import (
	"embed"
	"errors"
	"io/fs"
	"path"
	"sort"
)

//go:embed */SKILL.md
var files embed.FS

// ErrNotFound is returned by Get for an unknown skill name.
var ErrNotFound = errors.New("skill not found")

// SharedSites documents the client API and deploy flow. It is the skill the
// CLI installs and the server serves at /skill.md.
const SharedSites = "shared-sites"

// Get returns the SKILL.md body for a skill name.
func Get(name string) ([]byte, error) {
	// Reject any path trickery before it reaches the embedded FS: names come
	// from request paths on the server side.
	if name == "" || name != path.Base(name) || name == "." || name == ".." {
		return nil, ErrNotFound
	}
	b, err := files.ReadFile(path.Join(name, "SKILL.md"))
	if err != nil {
		return nil, ErrNotFound
	}
	return b, nil
}

// Names lists the embedded skills in sorted order.
func Names() []string {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}
