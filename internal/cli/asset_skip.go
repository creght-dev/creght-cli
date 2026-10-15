package cli

import (
	"fmt"
	"path"
	"strconv"
	"strings"
)

// assetSkip decides which asset files `creght export` leaves out of the
// package, e.g. desktop installers kept in a site's asset library. Skipped
// files are listed under missing_assets, so after import they keep pointing at
// their original URL on the source deployment.
type assetSkip struct {
	patterns []string // matched against the file name; "dmg" / ".dmg" mean "*.dmg"
	maxBytes int64    // 0: no limit
}

func parseAssetSkip(patterns, maxSize string) (assetSkip, error) {
	var s assetSkip
	for _, p := range strings.Split(patterns, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if !strings.ContainsAny(p, "*?[") {
			p = "*." + strings.TrimPrefix(p, ".") // bare extension
		}
		if _, err := path.Match(p, ""); err != nil {
			return s, fmt.Errorf("bad --skip-assets pattern %q: %w", p, err)
		}
		s.patterns = append(s.patterns, p)
	}
	if strings.TrimSpace(maxSize) != "" {
		n, err := parseByteSize(maxSize)
		if err != nil {
			return s, fmt.Errorf("bad --max-asset-size: %w", err)
		}
		s.maxBytes = n
	}
	return s, nil
}

func (s assetSkip) active() bool { return len(s.patterns) > 0 || s.maxBytes > 0 }

// reason returns why an asset is skipped, or "" to keep it. size may be 0 when
// the server does not know it; the download then enforces maxBytes.
func (s assetSkip) reason(key string, size int64) string {
	name := strings.ToLower(path.Base(key))
	for _, p := range s.patterns {
		if ok, _ := path.Match(p, name); ok {
			return "matches " + p
		}
	}
	if s.maxBytes > 0 && size > s.maxBytes {
		return fmt.Sprintf("larger than %s", formatMB(s.maxBytes))
	}
	return ""
}

// parseByteSize reads "20MB", "500KB", "1.5GB" or a plain number of bytes.
func parseByteSize(v string) (int64, error) {
	v = strings.ToUpper(strings.TrimSpace(v))
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		n      int64
	}{{"GB", 1 << 30}, {"G", 1 << 30}, {"MB", 1 << 20}, {"M", 1 << 20}, {"KB", 1 << 10}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(v, u.suffix) {
			v, mult = strings.TrimSpace(strings.TrimSuffix(v, u.suffix)), u.n
			break
		}
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("%q is not a size like 20MB", v)
	}
	return int64(f * float64(mult)), nil
}
