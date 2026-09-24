package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"
)

// runtimePackage is one importMap specifier as the site sees it at runtime.
type runtimePackage struct {
	URL    string `json:"url"`
	Source string `json:"source"` // "builtin" or the config file path
	// SSR reports whether server rendering can resolve the specifier. The
	// renderer only installs the platform built-ins, so a project-added
	// specifier works in the browser but not during SSR.
	SSR bool `json:"ssr"`
}

// renderConfigImportMapKeys are the render_config fields folded into
// "packages"; they are not printed again on their own.
var renderConfigImportMapKeys = map[string]bool{
	"import_map":        true,
	"dev_import_map":    true,
	"ignore_import_map": true,
}

// runRuntime prints the runtime a site runs on. "packages" is computed here
// (built-ins overlaid with the site's config); every other render_config field
// the platform sends, such as "limits", is passed through as-is, so new
// platform facts show up without a CLI release. An optional first positional
// argument selects one section.
func runRuntime(ctx context.Context, args []string) error {
	section := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		section, args = args[0], args[1:]
	}

	fs := flag.NewFlagSet("runtime", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	ref := fs.String("ref", "remote", "which config to read: remote | local")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if section == "" && fs.NArg() > 0 {
		section = fs.Arg(0)
	}

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}
	renderConfig, err := client.GetRenderConfigRaw(ctx)
	if err != nil {
		return fmt.Errorf("get system info: %w", err)
	}

	out := map[string]any{}
	for k, v := range renderConfig {
		if !renderConfigImportMapKeys[k] {
			out[k] = v
		}
	}

	// packages needs the site's config; skip that work when another section was asked for.
	if section == "" || section == "packages" {
		resolvedDir, resolvedSiteID, err := resolveSiteWorkspace(*dir, *siteID, !flagWasSet(fs, "dir"), false)
		if err != nil {
			return err
		}
		var builtin map[string]string
		if raw, ok := renderConfig["import_map"]; ok {
			if err := json.Unmarshal(raw, &builtin); err != nil {
				return fmt.Errorf("decode render_config.import_map: %w", err)
			}
		}
		imports, err := siteEffectiveImportMap(ctx, client, builtin, resolvedDir, resolvedSiteID, *ref)
		if err != nil {
			return err
		}
		out["packages"] = runtimePackages(builtin, imports)
	}

	if section == "" {
		return printJSON(out)
	}
	v, ok := out[section]
	if !ok {
		keys := make([]string, 0, len(out)+1)
		for k := range out {
			keys = append(keys, k)
		}
		if _, has := out["packages"]; !has {
			keys = append(keys, "packages")
		}
		sort.Strings(keys)
		return fmt.Errorf("unknown runtime section %q; available: %s", section, strings.Join(keys, ", "))
	}
	return printJSON(v)
}

func runtimePackages(builtin map[string]string, imports importMapOutput) map[string]runtimePackage {
	out := make(map[string]runtimePackage, len(imports.Imports))
	for spec, url := range imports.Imports {
		_, isBuiltin := builtin[spec]
		out[spec] = runtimePackage{URL: url, Source: imports.Sources[spec], SSR: isBuiltin}
	}
	return out
}
