package workspace

import (
	"net/url"
	"strings"
)

// CanonicalAPIHost normalizes an API host URL (scheme and host lowercased, no
// trailing slash, query or fragment) so two spellings of one deployment compare
// equal. Input that is not an absolute URL is returned trimmed.
func CanonicalAPIHost(apiHost string) string {
	apiHost = strings.TrimRight(strings.TrimSpace(apiHost), "/")
	if apiHost == "" {
		return ""
	}

	u, err := url.Parse(apiHost)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return apiHost
	}

	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery = ""
	u.Fragment = ""

	return strings.TrimRight(u.String(), "/")
}

// recordHost is the API host a state write stamps on the workspace: the one
// already recorded, if any, otherwise apiHost. The stamp is written once and
// never rewritten, so a one-off override cannot repoint a workspace.
func recordHost(recorded string, apiHost string) string {
	if host := CanonicalAPIHost(recorded); host != "" {
		return host
	}
	return CanonicalAPIHost(apiHost)
}
