package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
)

// A site answers on three kinds of address and users need all three: the
// preview host always serves the current remote workspace, the published
// domains serve whichever version is live, and the editor is where the site is
// opened in the browser. `creght url` prints them; nothing here opens a
// browser unless --open asks for it, because a command that hijacks the screen
// is no use over SSH, in CI, or under an agent.
type siteURLs struct {
	Preview string    `json:"preview,omitempty"`
	Live    []liveURL `json:"live"`
	Editor  string    `json:"editor,omitempty"`

	// liveErr records why the published domains are missing, when the rest of
	// the addresses resolved fine. Reported on stderr, never in the JSON.
	liveErr error
}

// liveURL is one published domain and the version it currently serves. Pinned
// domains stay on their own version instead of following the site default.
type liveURL struct {
	URL       string `json:"url"`
	VersionNo int64  `json:"version_no,omitempty"`
	VersionID int64  `json:"version_id,omitempty"`
	Pinned    bool   `json:"pinned"`
	System    bool   `json:"system"`
}

func runURL(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("url", flag.ContinueOnError)
	siteID, dir := siteTargetFlags(fs)
	open := fs.Bool("open", false, "open the preview URL in the browser")
	jsonOut := fs.Bool("json", false, "output the addresses as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("url does not accept positional arguments; use --site_id=<project_id>/<site_id>")
	}

	projectID, realSiteID, _, err := resolveSiteTarget(fs, *siteID, *dir, *jsonOut)
	if err != nil {
		return err
	}

	client, cfg, err := clientFromConfig()
	if err != nil {
		return err
	}

	urls, err := siteAddresses(ctx, client, cfg, projectID, realSiteID)
	if err != nil {
		return err
	}

	if *jsonOut {
		if err := printJSON(urls); err != nil {
			return err
		}
	} else {
		printSiteURLs(os.Stdout, urls)
	}
	if urls.liveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read the published domains: %v\n", urls.liveErr)
	}

	if *open {
		if urls.Preview == "" {
			return fmt.Errorf("preview URL is unavailable")
		}
		return openBrowser(urls.Preview)
	}
	return nil
}

// siteAddresses collects every address for a site. A failure to read the
// publish state is not fatal: the preview and editor URLs are still worth
// printing, and legacy sites can lack a publish panel entirely.
func siteAddresses(ctx context.Context, client *creght.Client, cfg Config, projectID string, siteID string) (siteURLs, error) {
	info, err := client.GetSystemInfo(ctx)
	if err != nil {
		return siteURLs{}, err
	}

	urls := siteURLs{
		Preview: previewURLForHost(info.SelfAPIHost, siteID),
		Editor:  siteEditorURL(defaultWebHost(cfg.APIHost), projectID, siteID),
		Live:    []liveURL{},
	}

	state, err := client.GetSitePublishState(ctx, projectID, siteID)
	if err != nil {
		urls.liveErr = err
		return urls, nil
	}
	urls.Live = liveURLs(state, schemeOf(info.SelfAPIHost))

	return urls, nil
}

// liveURLs turns the publish panel into addresses. Domains that follow the site
// default serve the current version; pinned ones serve their own.
func liveURLs(state creght.SitePublishState, scheme string) []liveURL {
	out := []liveURL{}
	for _, domain := range state.Domains {
		address := domainURL(scheme, domain.Domain)
		if address == "" {
			continue
		}
		live := liveURL{
			URL:       address,
			VersionNo: domain.PublishVersionNo,
			VersionID: domain.PublishVersionID,
			Pinned:    !domain.Follow,
			System:    domain.System,
		}
		if domain.Follow {
			live.VersionNo, live.VersionID = state.CurrentVersionNo, state.CurrentVersionID
		}
		out = append(out, live)
	}
	if len(out) == 0 {
		// Older publish states report only the system domain.
		if address := domainURL(scheme, state.SystemDomain); address != "" {
			out = append(out, liveURL{
				URL:       address,
				VersionNo: state.CurrentVersionNo,
				VersionID: state.CurrentVersionID,
				System:    true,
			})
		}
	}

	return out
}

// domainURL makes a browsable URL out of a bare hostname from the publish
// panel. The scheme follows the deployment's own, so a local backend prints
// http:// links instead of https:// ones nothing is listening on.
func domainURL(scheme string, domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	if strings.Contains(domain, "://") {
		return strings.TrimRight(domain, "/") + "/"
	}
	if scheme == "" {
		scheme = "https"
	}

	return scheme + "://" + strings.TrimRight(domain, "/") + "/"
}

func schemeOf(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" {
		return "https"
	}

	return u.Scheme
}

func printSiteURLs(out io.Writer, urls siteURLs) {
	w := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
	defer func() { _ = w.Flush() }()

	if urls.Preview != "" {
		printURLRow(w, "Preview:", urls.Preview, "")
	}
	switch {
	case urls.liveErr != nil:
		printURLRow(w, "Live:", "unavailable", "")
	case len(urls.Live) == 0:
		printURLRow(w, "Live:", "nothing published yet; run creght publish", "")
	default:
		for i, live := range urls.Live {
			label := "Live:"
			if i > 0 {
				label = ""
			}
			printURLRow(w, label, live.URL, liveURLNote(live))
		}
	}
	if urls.Editor != "" {
		printURLRow(w, "Editor:", urls.Editor, "")
	}
}

// printURLRow keeps the label column aligned. A row without a note leaves its
// URL untabbed so the line carries no trailing padding.
func printURLRow(w io.Writer, label string, address string, note string) {
	if note == "" {
		fmt.Fprintf(w, "%s\t%s\n", label, address)
		return
	}
	fmt.Fprintf(w, "%s\t%s\t%s\n", label, address, note)
}

func liveURLNote(live liveURL) string {
	var label string
	switch {
	case live.VersionNo > 0:
		label = fmt.Sprintf("v%d", live.VersionNo)
	case live.VersionID > 0:
		label = fmt.Sprintf("id %d", live.VersionID)
	default:
		label = "not published"
	}
	if live.Pinned && live.VersionID > 0 {
		label += " (pinned)"
	}

	return label
}
