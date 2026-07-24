package cli

import (
	"bufio"
	"bysir/creght-cli/internal/creght"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
