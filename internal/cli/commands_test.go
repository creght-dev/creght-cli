package cli

import (
	"context"
	"strings"
	"testing"
)

func TestWorkspaceCommandHelpExplainsOptionalSiteAndDir(t *testing.T) {
	root := newRootCommand(context.Background(), nil)

	for _, name := range []string{"pull", "diff", "push"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if !strings.Contains(cmd.Long, ".creght/state.json") {
			t.Fatalf("%s long help does not explain workspace discovery: %q", name, cmd.Long)
		}
		if !strings.Contains(cmd.Example, "creght "+name) {
			t.Fatalf("%s examples missing short workspace command: %q", name, cmd.Example)
		}
		siteFlag := cmd.Flags().Lookup("site_id")
		if siteFlag == nil || !strings.Contains(siteFlag.Usage, "Optional inside a pulled workspace") {
			t.Fatalf("%s --site_id help does not explain optional usage", name)
		}
	}
}
