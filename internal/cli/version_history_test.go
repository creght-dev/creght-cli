package cli

import (
	"bysir/creght-cli/internal/creght"
	"strings"
	"testing"
)

func f(id, body string) creght.File { return creght.File{ID: id, Body: body} }

// 回滚计划里每一条都直接作用于线上文件：漏一条 delete，v190 之后新增的文件会留下来，
// 结果既不是 v190 也不是当前状态；多一条 delete，会删掉本不该动的东西。
func TestRollbackPlanCoversRestoreRevertAndDelete(t *testing.T) {
	target := map[string]creght.File{
		"/keep.tsx":    f("1", "same"),
		"/changed.tsx": f("2", "old body"),
		"/gone.tsx":    f("3", "was deleted after v190"),
	}
	live := map[string]creght.File{
		"/keep.tsx":    f("1", "same"),
		"/changed.tsx": f("2", "new body"),
		"/added.tsx":   f("4", "added after v190"),
	}

	changes, summary := rollbackPlan(target, live)
	if len(changes) != 3 {
		t.Fatalf("changes = %d, want 3:\n%s", len(changes), strings.Join(summary, "\n"))
	}

	byPath := map[string]creght.SiteActionChange{}
	for i, c := range changes {
		key := summary[i]
		byPath[strings.TrimSpace(strings.SplitN(key, " ", 2)[1])] = c
	}

	// v190 之后被删掉的文件要恢复，用 create（它在线上已经没有 id 了）。
	if got := byPath["/gone.tsx"]; got.Action != "file_create" || got.File.Path == nil || *got.File.Body != "was deleted after v190" {
		t.Fatalf("/gone.tsx = %+v, want a file_create carrying the old body", got)
	}
	// 内容变了的用 update，且必须带线上那个 id，不是版本里的。
	if got := byPath["/changed.tsx"]; got.Action != "file_update" || got.File.ID != "2" || *got.File.Body != "old body" {
		t.Fatalf("/changed.tsx = %+v, want a file_update to the old body", got)
	}
	// v190 之后新增的必须删掉，否则回滚是假的。
	if got := byPath["/added.tsx"]; got.Action != "file_delete" || got.File.ID != "4" {
		t.Fatalf("/added.tsx = %+v, want a file_delete", got)
	}
	// 没变的一条指令都不该发。
	if _, touched := byPath["/keep.tsx"]; touched {
		t.Fatal("an unchanged file was included in the plan")
	}
}

func TestRollbackPlanIsEmptyWhenAlreadyAtThatVersion(t *testing.T) {
	same := map[string]creght.File{"/a.tsx": f("1", "x"), "/b.tsx": f("2", "y")}
	changes, _ := rollbackPlan(same, same)
	if len(changes) != 0 {
		t.Fatalf("changes = %d, want none", len(changes))
	}
}

// 路径要在比较之前归一化，否则 "page/a.tsx" 和 "/page/a.tsx" 会被当成两个文件 ——
// 一边删一边建，同一个文件被推倒重来。
func TestNormalizeSitePath(t *testing.T) {
	for in, want := range map[string]string{
		"page/a.tsx":    "/page/a.tsx",
		"/page/a.tsx":   "/page/a.tsx",
		"page\\a.tsx":   "/page/a.tsx",
		"/page//a.tsx":  "/page/a.tsx",
		"/page/./a.tsx": "/page/a.tsx",
	} {
		if got := normalizeSitePath(in); got != want {
			t.Fatalf("normalizeSitePath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortedPathsIsUnionAndStable(t *testing.T) {
	a := map[string]creght.File{"/b": {}, "/a": {}}
	b := map[string]creght.File{"/c": {}, "/a": {}}
	got := sortedPaths(a, b)
	want := []string{"/a", "/b", "/c"}
	if len(got) != len(want) {
		t.Fatalf("sortedPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortedPaths = %v, want %v", got, want)
		}
	}
}
