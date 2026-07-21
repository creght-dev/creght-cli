package cli

import "testing"

func TestEffectiveImportMapOverlaysConfig(t *testing.T) {
	builtin := map[string]string{
		"react":         "https://esm.sh/react@19",
		"talizen/":      "https://esm.sh/talizen@0.1.3/",
		"framer-motion": "https://esm.sh/framer-motion@11.0.0",
	}
	config := `
const icon = "x"
export default {
  metadata: { title: "hi" },
  importMap: {
    imports: {
      "framer-motion": "https://esm.sh/framer-motion@12.0.0",
      "my-lib": "https://cdn.example.com/my-lib.js",
    },
  },
}`

	out, err := effectiveImportMap(builtin, "/talizen.config.ts", config)
	if err != nil {
		t.Fatalf("effectiveImportMap: %v", err)
	}

	if got := out.Imports["react"]; got != "https://esm.sh/react@19" {
		t.Errorf("react = %q, want built-in url", got)
	}
	if got := out.Imports["talizen/"]; got != "https://esm.sh/talizen@0.1.3/" {
		t.Errorf("talizen/ = %q, want built-in url", got)
	}
	if got := out.Imports["framer-motion"]; got != "https://esm.sh/framer-motion@12.0.0" {
		t.Errorf("framer-motion = %q, want config override", got)
	}
	if got := out.Imports["my-lib"]; got != "https://cdn.example.com/my-lib.js" {
		t.Errorf("my-lib = %q, want config url", got)
	}
	if got := out.Sources["react"]; got != "builtin" {
		t.Errorf("sources[react] = %q, want builtin", got)
	}
	if got := out.Sources["framer-motion"]; got != "/talizen.config.ts" {
		t.Errorf("sources[framer-motion] = %q, want config path", got)
	}
}

func TestEffectiveImportMapNoConfig(t *testing.T) {
	builtin := map[string]string{"react": "https://esm.sh/react@19"}
	out, err := effectiveImportMap(builtin, "", "")
	if err != nil {
		t.Fatalf("effectiveImportMap: %v", err)
	}
	if len(out.Imports) != 1 || out.Imports["react"] != "https://esm.sh/react@19" {
		t.Errorf("imports = %v, want only built-in react", out.Imports)
	}
	if out.Sources["react"] != "builtin" {
		t.Errorf("sources[react] = %q, want builtin", out.Sources["react"])
	}
}

func TestParseConfigImportMapDefineConfig(t *testing.T) {
	config := `
import { defineConfig } from 'talizen'
export default defineConfig({
  importMap: { imports: { "cobe": "https://esm.sh/cobe@2.0.1" } },
})`
	imports, err := parseConfigImportMap("/talizen.config.ts", config)
	if err != nil {
		t.Fatalf("parseConfigImportMap: %v", err)
	}
	if imports["cobe"] != "https://esm.sh/cobe@2.0.1" {
		t.Errorf("cobe = %q, want config url", imports["cobe"])
	}
}
