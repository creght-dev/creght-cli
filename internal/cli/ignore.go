package cli

import (
	"bufio"
	"bysir/creght-cli/internal/creght"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const creghtIgnoreFileName = ".creghtignore"

type creghtIgnoreRule struct {
	negated bool
	pattern *regexp.Regexp
}

type creghtIgnore struct {
	rules []creghtIgnoreRule
}

func loadCreghtIgnore(root string) (*creghtIgnore, error) {
	body, err := os.ReadFile(filepath.Join(root, creghtIgnoreFileName))
	if os.IsNotExist(err) {
		return &creghtIgnore{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", creghtIgnoreFileName, err)
	}

	ignore := &creghtIgnore{}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
			if line == "" {
				continue
			}
		}
		if strings.HasPrefix(line, `\#`) || strings.HasPrefix(line, `\!`) {
			line = line[1:]
		}

		pattern, err := compileCreghtIgnorePattern(line)
		if err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", creghtIgnoreFileName, lineNumber, err)
		}
		ignore.rules = append(ignore.rules, creghtIgnoreRule{negated: negated, pattern: pattern})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", creghtIgnoreFileName, err)
	}
	return ignore, nil
}

func compileCreghtIgnorePattern(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(strings.TrimSpace(pattern))
	rootAnchored := strings.HasPrefix(pattern, "/")
	pattern = strings.TrimPrefix(pattern, "/")
	pattern = strings.TrimSuffix(pattern, "/")
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}

	anchored := rootAnchored || strings.Contains(pattern, "/")
	var out strings.Builder
	if anchored {
		out.WriteString("^")
	} else {
		out.WriteString("(?:^|.*/)")
	}

	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				for i+1 < len(pattern) && pattern[i+1] == '*' {
					i++
				}
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					out.WriteString("(?:.*/)?")
					i += 2
					continue
				}
				out.WriteString(".*")
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		case '\\':
			if i+1 < len(pattern) {
				i++
				out.WriteString(regexp.QuoteMeta(string(pattern[i])))
			} else {
				out.WriteString(`\\`)
			}
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
		i++
	}
	// A pattern that matches a directory also ignores everything below it.
	out.WriteString("(?:/.*)?$")
	return regexp.Compile(out.String())
}

func (i *creghtIgnore) matches(remotePath string) bool {
	path := strings.TrimPrefix(filepath.ToSlash(remotePath), "/")
	if path == creghtIgnoreFileName {
		return true
	}

	ignored := false
	for _, rule := range i.rules {
		if rule.pattern.MatchString(path) {
			ignored = !rule.negated
		}
	}
	return ignored
}

// ignoredRemotePaths lists remote paths that .creghtignore hides from sync.
//
// These files stay on the site. push neither uploads nor deletes them, and
// saveWorkspaceState drops their base entries, so no later plan has a base to
// delete from — a rule added before the remote copy was removed leaves that
// copy live and out of the CLI's reach. Reporting the paths is the difference
// between "stopped syncing" and "still on your site"; dropping them silently
// is what makes the ordering a trap. creght rm deletes one regardless.
func ignoredRemotePaths(ignore *creghtIgnore, files []creght.File) []string {
	var out []string
	for _, file := range files {
		if file.IsDir {
			continue
		}
		if strings.TrimPrefix(filepath.ToSlash(file.Path), "/") == creghtIgnoreFileName {
			// Never synced by design, so its absence is not a surprise.
			continue
		}
		if ignore.matches(file.Path) {
			out = append(out, file.Path)
		}
	}
	sort.Strings(out)
	return out
}

func filterIgnoredSnapshot(ignore *creghtIgnore, files map[string]snapshotEntry) map[string]snapshotEntry {
	filtered := make(map[string]snapshotEntry, len(files))
	for path, entry := range files {
		if ignore.matches(path) {
			continue
		}
		filtered[path] = entry
	}
	return filtered
}

func filterIgnoredState(ignore *creghtIgnore, files map[string]stateEntry) map[string]stateEntry {
	filtered := make(map[string]stateEntry, len(files))
	for path, entry := range files {
		if ignore.matches(path) {
			continue
		}
		filtered[path] = entry
	}
	return filtered
}

func remoteFileSnapshotForWorkspace(root string, files []creght.File) (map[string]snapshotEntry, error) {
	ignore, err := loadCreghtIgnore(root)
	if err != nil {
		return nil, err
	}
	return filterIgnoredSnapshot(ignore, remoteFileSnapshot(files)), nil
}
