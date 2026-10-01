package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// UI reference library: a curated set of well-designed real sites, tagged by
// industry / site type / page type. It is a public Creght site, so these
// commands need no login and do not follow CREGHT_API_HOST.
const defaultUIRefsURL = "https://p92jhkfr126a.site.creght.cn"

// Downloaded images are resized by the CDN: wide enough to read the layout,
// small enough for an agent's image reader.
const uiRefsSaveWidth = 1200

func uiRefsURL() string {
	if v := strings.TrimSpace(os.Getenv("CREGHT_UI_REFS_URL")); v != "" {
		return strings.TrimRight(v, "/")
	}
	return defaultUIRefsURL
}

type uiRefsVocab struct {
	Industry []string `json:"industry"`
	SiteType []string `json:"siteType"`
	PageType []string `json:"pageType"`
}

type uiRef struct {
	ID       string   `json:"id"`
	URL      string   `json:"url"`
	Width    int      `json:"width,omitempty"`
	Height   int      `json:"height,omitempty"`
	Source   string   `json:"source,omitempty"`
	PageURL  string   `json:"pageUrl,omitempty"`
	Industry []string `json:"industry,omitempty"`
	SiteType string   `json:"siteType,omitempty"`
	PageType string   `json:"pageType,omitempty"`
	Style    []string `json:"style,omitempty"`
	Features []string `json:"features,omitempty"`
	Colors   []string `json:"colors,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Saved    string   `json:"saved,omitempty"`
}

type uiRefsSearchResult struct {
	MatchedBy []string `json:"matchedBy"`
	Dropped   []string `json:"dropped"`
	List      []uiRef  `json:"list"`
}

var uiRefsHTTP = &http.Client{Timeout: 30 * time.Second}

func callUIRefs(ctx context.Context, method string, input any, out any) error {
	body, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uiRefsURL()+"/func/refs."+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := uiRefsHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("reference library unreachable: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("reference library: status %d: %s", resp.StatusCode, truncateRefsText(string(raw), 200))
	}
	if env.Error != "" {
		return fmt.Errorf("reference library: %s", env.Error)
	}
	return json.Unmarshal(env.Result, out)
}

func truncateRefsText(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func runRefs(ctx context.Context, args []string) error {
	if len(args) == 0 {
		printRefsUsage()
		return nil
	}
	switch args[0] {
	case "search":
		return runRefsSearch(ctx, args[1:])
	case "vocab":
		return runRefsVocab(ctx, args[1:])
	case "help", "-h", "--help":
		printRefsUsage()
		return nil
	default:
		return fmt.Errorf("unknown refs command: %s", args[0])
	}
}

func printRefsUsage() {
	fmt.Println(`creght refs

Usage:
  creght refs search --industry=<industry> [--site_type=<type>] [--page_type=<type>] [--limit=12] [--save=<dir>] [--json]
  creght refs vocab [--json]

Notes:
  Search a curated library of well-designed real sites for visual references
  before deciding a new site's look. Values come from creght refs vocab.
  --save downloads the images (resized to 1200px wide) so you can open them.`)
}

func runRefsVocab(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("refs vocab", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "print the vocabulary as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var vocab uiRefsVocab
	if err := callUIRefs(ctx, "vocab", map[string]any{}, &vocab); err != nil {
		return err
	}
	if *jsonOut {
		return printJSON(vocab)
	}
	fmt.Printf("industry:\n  %s\n\n", strings.Join(vocab.Industry, "\n  "))
	fmt.Printf("site_type:\n  %s\n\n", strings.Join(vocab.SiteType, "\n  "))
	fmt.Printf("page_type:\n  %s\n", strings.Join(vocab.PageType, "\n  "))
	return nil
}

// checkUIRefsValue rejects a value outside the vocabulary: the library matches
// tags exactly, and a near-miss ("SaaS" for "科技 / SaaS / 互联网") would
// silently fall back to looser results.
func checkUIRefsValue(flagName, value string, allowed []string) error {
	if value == "" || slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("--%s=%q is not in the vocabulary; use one of:\n  %s", flagName, value, strings.Join(allowed, "\n  "))
}

func runRefsSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("refs search", flag.ContinueOnError)
	industry := fs.String("industry", "", "the site's industry (required)")
	siteType := fs.String("site_type", "", "the kind of site")
	pageType := fs.String("page_type", "", "only when designing one specific page or asset")
	limit := fs.Int("limit", 12, "how many references, max 16")
	save := fs.String("save", "", "download the images into this directory")
	jsonOut := fs.Bool("json", false, "print the result as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*industry, *siteType, *pageType = strings.TrimSpace(*industry), strings.TrimSpace(*siteType), strings.TrimSpace(*pageType)
	if *industry == "" {
		return fmt.Errorf("--industry is required; see creght refs vocab")
	}

	var vocab uiRefsVocab
	if err := callUIRefs(ctx, "vocab", map[string]any{}, &vocab); err != nil {
		return err
	}
	for _, c := range []struct {
		name, value string
		allowed     []string
	}{{"industry", *industry, vocab.Industry}, {"site_type", *siteType, vocab.SiteType}, {"page_type", *pageType, vocab.PageType}} {
		if err := checkUIRefsValue(c.name, c.value, c.allowed); err != nil {
			return err
		}
	}

	var res uiRefsSearchResult
	if err := callUIRefs(ctx, "search", map[string]any{
		"industry": *industry, "siteType": *siteType, "pageType": *pageType, "limit": *limit,
	}, &res); err != nil {
		return err
	}
	// Report conditions with the flag names the user typed.
	rename := strings.NewReplacer("siteType", "site_type", "pageType", "page_type")
	for i := range res.MatchedBy {
		res.MatchedBy[i] = rename.Replace(res.MatchedBy[i])
	}
	for i := range res.Dropped {
		res.Dropped[i] = rename.Replace(res.Dropped[i])
	}

	if *save != "" && len(res.List) > 0 {
		if err := saveUIRefs(ctx, *save, res.List); err != nil {
			return err
		}
	}

	if *jsonOut {
		return printJSON(res)
	}
	printUIRefs(res, *save != "")
	return nil
}

func saveUIRefs(ctx context.Context, dir string, refs []uiRef) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	var wg sync.WaitGroup
	errs := make([]error, len(refs))
	sem := make(chan struct{}, 4)
	for i := range refs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			path := filepath.Join(dir, refs[i].ID+".jpg")
			if err := downloadUIRef(ctx, refs[i].URL, path); err != nil {
				errs[i] = fmt.Errorf("%s: %w", refs[i].ID, err)
				return
			}
			refs[i].Saved = path
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			// One failed download should not hide the rest; report it and keep going.
			fmt.Fprintf(os.Stderr, "warning: download failed: %v\n", err)
		}
	}
	return nil
}

func downloadUIRef(ctx context.Context, src, path string) error {
	sep := "?"
	if strings.Contains(src, "?") {
		sep = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s%sw=%d&fmt=jpg", src, sep, uiRefsSaveWidth), nil)
	if err != nil {
		return err
	}
	resp, err := uiRefsHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func printUIRefs(res uiRefsSearchResult, saved bool) {
	if len(res.List) == 0 {
		fmt.Println("No reference covers this yet. Decide the visual direction yourself.")
		return
	}
	fmt.Printf("matched by: %s\n", strings.Join(res.MatchedBy, ", "))
	if len(res.Dropped) > 0 {
		fmt.Printf("dropped: %s (no exact match, so these fit only on %s)\n", strings.Join(res.Dropped, ", "), strings.Join(res.MatchedBy, ", "))
	}
	fmt.Println()
	for i, r := range res.List {
		tags := []string{}
		if r.PageType != "" {
			tags = append(tags, r.PageType)
		}
		if len(r.Style) > 0 {
			tags = append(tags, strings.Join(r.Style, " / "))
		}
		fmt.Printf("%d. %s  %s\n", i+1, r.Source, strings.Join(tags, " · "))
		if r.Summary != "" {
			fmt.Printf("   %s\n", r.Summary)
		}
		if r.Saved != "" {
			fmt.Printf("   saved: %s\n", r.Saved)
		} else {
			fmt.Printf("   %s (%dx%d)\n", r.URL, r.Width, r.Height)
		}
	}
	fmt.Println()
	if saved {
		fmt.Println("These differ in style on purpose. Open the 2-3 closest to what the user asked for before choosing palette, layout and imagery; borrow the direction, never their text, logos or photos.")
	} else {
		fmt.Println("These differ in style on purpose. Look at the 2-3 closest to what the user asked for (add --save=<dir> to download them) before choosing palette, layout and imagery; borrow the direction, never their text, logos or photos.")
	}
}
