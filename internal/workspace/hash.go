package workspace

import (
	"fmt"
	"path"
	"strings"

	"github.com/br41n10/qetag"
)

// qetagHash is the content hash the platform reports for site files; local
// bodies are hashed the same way so the two can be compared.
func QetagHash(body []byte) (string, error) {
	qe := qetag.New()
	_, err := qe.Write(body)
	if err != nil {
		return "", fmt.Errorf("qetag hash: %w", err)
	}
	return qe.Etag(), nil
}

// normalizeSitePath turns a site path into its canonical "/a/b" form.
func NormalizeSitePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return path.Clean(p)
}
