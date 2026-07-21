package cli

import (
	"bysir/creght-cli/internal/creght"
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dop251/goja"
	esbuild "github.com/evanw/esbuild/pkg/api"
)

// importMapConfigFiles lists the site config files that may declare an
// importMap, in the platform's precedence order (first match wins).
var importMapConfigFiles = []string{
	"/talizen.config.ts", "/talizen.config.js", "/talizen.config.mjs", "/talizen.config.cjs",
	"/creght.config.ts", "/creght.config.js", "/creght.config.mjs", "/creght.config.cjs",
	"/folia.config.ts", "/folia.config.js", "/folia.config.mjs", "/folia.config.cjs",
}

// importMapOutput is the effective importMap: the resolved specifier->URL map
// plus, per specifier, where it came from ("builtin" or the config file path).
type importMapOutput struct {
	Imports map[string]string `json:"imports"`
	Sources map[string]string `json:"sources"`
}

// runImportMap prints a site's effective importMap: the platform built-in
// imports (from /system/info) overlaid with the project's talizen.config
// importMap.imports, mirroring how the renderer composes it. It reads the
// config from the remote site (default) or the local workspace (--ref local).
func runImportMap(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("importmap", flag.ContinueOnError)
	siteID := fs.String("site_id", "", "project_id/site_id")
	dir := fs.String("dir", ".", "local directory")
	ref := fs.String("ref", "remote", "which config to read: remote | local")
	if err := fs.Parse(args); err != nil {
		return err
	}

	resolvedDir, resolvedSiteID, err := resolveSiteWorkspace(*dir, *siteID, !flagWasSet(fs, "dir"), false)
	if err != nil {
		return err
	}
	*dir, *siteID = resolvedDir, resolvedSiteID

	client, _, err := clientFromConfig()
	if err != nil {
		return err
	}

	info, err := client.GetSystemInfo(ctx)
	if err != nil {
		return fmt.Errorf("get system info: %w", err)
	}

	var configPath, configBody string
	switch *ref {
	case "remote":
		projectID, realSiteID, err := parseSiteRef(*siteID)
		if err != nil {
			return err
		}
		files, err := client.GetFileList(ctx, projectID, realSiteID)
		if err != nil {
			return err
		}
		configPath, configBody = findConfigInRemote(files.List)
	case "local":
		localFiles, err := localFileSnapshot(*dir)
		if err != nil {
			return err
		}
		configPath, configBody = findConfigInSnapshot(localFiles)
	default:
		return fmt.Errorf("--ref must be remote or local, got %q", *ref)
	}

	result, err := effectiveImportMap(info.RenderConfig.ImportMap, configPath, configBody)
	if err != nil {
		return err
	}
	return printJSON(result)
}

func findConfigInRemote(files []creght.File) (path string, body string) {
	bodies := map[string]string{}
	for _, f := range files {
		if !f.IsDir {
			bodies[f.Path] = f.Body
		}
	}
	return findConfigBody(bodies)
}

func findConfigInSnapshot(files map[string]snapshotEntry) (path string, body string) {
	bodies := map[string]string{}
	for p, e := range files {
		bodies[p] = e.Body
	}
	return findConfigBody(bodies)
}

func findConfigBody(bodies map[string]string) (path string, body string) {
	for _, name := range importMapConfigFiles {
		if b, ok := bodies[name]; ok {
			return name, b
		}
	}
	return "", ""
}

// effectiveImportMap overlays the project config imports on top of the platform
// built-ins, tracking each specifier's origin. Later entries win, so config
// imports override built-ins with the same specifier.
func effectiveImportMap(builtin map[string]string, configPath, configBody string) (importMapOutput, error) {
	imports := map[string]string{}
	sources := map[string]string{}
	for k, v := range builtin {
		imports[k] = v
		sources[k] = "builtin"
	}

	if strings.TrimSpace(configBody) != "" {
		configImports, err := parseConfigImportMap(configPath, configBody)
		if err != nil {
			return importMapOutput{}, err
		}
		for k, v := range configImports {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" || v == "" {
				continue
			}
			imports[k] = v
			sources[k] = configPath
		}
	}

	return importMapOutput{Imports: imports, Sources: sources}, nil
}

// parseConfigImportMap evaluates a talizen.config file and returns its
// importMap.imports. The config is TypeScript/JS, so it is transformed to
// CommonJS with esbuild and executed in a goja VM (as the platform does),
// rather than parsed textually.
func parseConfigImportMap(path, body string) (map[string]string, error) {
	loader := esbuild.LoaderTS
	switch strings.ToLower(filepath.Ext(path)) {
	case ".js", ".mjs", ".cjs":
		loader = esbuild.LoaderJS
	}

	result := esbuild.Transform(body, esbuild.TransformOptions{
		Loader:     loader,
		Format:     esbuild.FormatCommonJS,
		Target:     esbuild.ES2015,
		Sourcefile: path,
	})
	if len(result.Errors) > 0 {
		return nil, fmt.Errorf("parse %s: %s", path, result.Errors[0].Text)
	}

	vm := goja.New()
	module := vm.NewObject()
	exports := vm.NewObject()
	_ = module.Set("exports", exports)
	_ = vm.Set("module", module)
	_ = vm.Set("exports", exports)
	identity := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		return call.Arguments[0]
	}
	_ = vm.Set("defineConfig", identity)
	// Stub require so configs that `import { defineConfig } from 'talizen'`
	// (compiled to a require call) still evaluate.
	_ = vm.Set("require", func(call goja.FunctionCall) goja.Value {
		stub := vm.NewObject()
		_ = stub.Set("defineConfig", identity)
		return stub
	})

	wrapped := "(function(module, exports, require){\n" + string(result.Code) + "\n})(module, exports, require);"
	if _, err := vm.RunString(wrapped); err != nil {
		return nil, fmt.Errorf("evaluate %s: %w", path, err)
	}

	exported := module.Get("exports")
	if exported == nil || goja.IsNull(exported) || goja.IsUndefined(exported) {
		return nil, nil
	}
	root, ok := exported.Export().(map[string]interface{})
	if !ok {
		return nil, nil
	}
	config, ok := root["default"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	importMap, ok := config["importMap"].(map[string]interface{})
	if !ok {
		return nil, nil
	}
	rawImports, ok := importMap["imports"].(map[string]interface{})
	if !ok {
		return nil, nil
	}

	out := map[string]string{}
	for k, v := range rawImports {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out, nil
}
