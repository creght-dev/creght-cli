package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tplProjectListBody = `{
	"total": 2,
	"list": [
		{
			"tpl": {
				"id": 12,
				"type": "tpl_project",
				"status": "online",
				"name_locales": {"zh-CN": "开源软件官网", "en": "Open Source Homepage"},
				"desc_locales": {"zh-CN": "适合开源项目的官网模板"},
				"use_count": 35,
				"categories": [{"id": 3, "name_locales": {"zh-CN": "科技"}}]
			},
			"project": {"id": "project-tpl-1", "name": "oss-homepage"},
			"site_list": [{"id": "site-tpl-1", "name": "main", "free_domain": "https://oss.creght.site"}],
			"preview_url": "https://oss.creght.site"
		},
		{
			"tpl": {
				"id": "13",
				"type": "tpl_project",
				"name_locales": {"en": "Restaurant"},
				"preview_url": "https://food.creght.site"
			},
			"project": {"id": "project-tpl-2", "name": "restaurant"}
		}
	]
}`

func TestRunTplList(t *testing.T) {
	setupTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/u/tpl/project" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("category_id"); got != "3" {
			t.Fatalf("category_id = %q, want 3", got)
		}
		if got := r.URL.Query().Get("limit"); got != "10" {
			t.Fatalf("limit = %q, want 10", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tplProjectListBody))
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTpl(context.Background(), []string{"list", "--category_id=3", "--limit=10"})
	})
	if err != nil {
		t.Fatalf("runTpl list: %v", err)
	}
	for _, want := range []string{
		"#12", "开源软件官网", "适合开源项目的官网模板", "categories: 科技",
		"preview: https://oss.creght.site", "used: 35",
		"#13", "Restaurant", "preview: https://food.creght.site",
		"total: 2", "creght tpl use <id>",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTplListRecommendJSON(t *testing.T) {
	setupTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/u/tpl/project_recommend" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tplProjectListBody))
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTpl(context.Background(), []string{"list", "--recommend", "--json"})
	})
	if err != nil {
		t.Fatalf("runTpl list --recommend --json: %v", err)
	}

	var res struct {
		Total int64 `json:"total"`
		List  []struct {
			Tpl struct {
				ID string `json:"id"`
			} `json:"tpl"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output)
	}
	if res.Total != 2 || len(res.List) != 2 || res.List[0].Tpl.ID != "12" {
		t.Fatalf("unexpected JSON output: %s", output)
	}
}

func TestRunTplGet(t *testing.T) {
	setupTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/u/tpl/project_detail" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("id"); got != "12" {
			t.Fatalf("id = %q, want 12", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"tpl": {"id": 12, "name_locales": {"zh-CN": "开源软件官网"}},
			"project": {"id": "project-tpl-1", "name": "oss-homepage"},
			"site_list": [{"id": "site-tpl-1", "name": "main", "free_domain": "https://oss.creght.site"}],
			"cms_list": [{"id": "cms-1", "key": "blog", "name": "Blog"}],
			"preview_url": "https://oss.creght.site"
		}`))
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTpl(context.Background(), []string{"get", "12"})
	})
	if err != nil {
		t.Fatalf("runTpl get: %v", err)
	}
	for _, want := range []string{
		"#12", "开源软件官网",
		"site-tpl-1", "https://oss.creght.site",
		"blog", "Blog",
		"creght tpl use 12 --name=<project_name>",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTplCategories(t *testing.T) {
	setupTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/u/tpl_category_list" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"list": [
			{"id": 1, "name_locales": {"zh-CN": "官网"}},
			{"id": 3, "pid": 1, "name_locales": {"zh-CN": "科技"}},
			{"id": 4, "name_locales": {"en": "E-commerce"}}
		]}`))
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTpl(context.Background(), []string{"categories"})
	})
	if err != nil {
		t.Fatalf("runTpl categories: %v", err)
	}
	for _, want := range []string{"#1\t官网", "  #3\t科技", "#4\tE-commerce", "--category_id=<id>"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTplUse(t *testing.T) {
	setupTestConfig(t)

	var gotCreate map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/u/project":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotCreate); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"project-9"}`))
		case "/api/u/project_list":
			_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"project-9","name":"My OSS Site","site_list":[{"id":"site-9","name":"main"}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTpl(context.Background(), []string{"use", "12", "--name=My OSS Site"})
	})
	if err != nil {
		t.Fatalf("runTpl use: %v", err)
	}
	if gotCreate["name"] != "My OSS Site" {
		t.Fatalf("create name = %#v", gotCreate["name"])
	}
	if gotCreate["tpl_id"] != float64(12) {
		t.Fatalf("create tpl_id = %#v, want 12", gotCreate["tpl_id"])
	}
	for _, want := range []string{
		"Created project project-9",
		"from template #12",
		"project-9/site-9\tmain",
		"Next: creght pull --site_id=project-9/site-9",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}

func TestRunTplUseJSON(t *testing.T) {
	setupTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/u/project":
			_, _ = w.Write([]byte(`{"id":"project-9"}`))
		case "/api/u/project_list":
			_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"project-9","name":"My OSS Site","site_list":[{"id":"site-9","name":"main"}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTpl(context.Background(), []string{"use", "12", "--name=My OSS Site", "--json"})
	})
	if err != nil {
		t.Fatalf("runTpl use --json: %v", err)
	}

	var res struct {
		ProjectID string `json:"project_id"`
		TplID     int64  `json:"tpl_id"`
		Sites     []struct {
			SiteID string `json:"site_id"`
			Name   string `json:"name"`
		} `json:"sites"`
	}
	if err := json.Unmarshal([]byte(output), &res); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output)
	}
	if res.ProjectID != "project-9" || res.TplID != 12 {
		t.Fatalf("unexpected JSON output: %s", output)
	}
	if len(res.Sites) != 1 || res.Sites[0].SiteID != "project-9/site-9" {
		t.Fatalf("unexpected sites in JSON output: %s", output)
	}
}

func TestRunTplUseRequiresName(t *testing.T) {
	setupTestConfig(t)
	err := runTpl(context.Background(), []string{"use", "12"})
	if err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("err = %v, want --name requirement", err)
	}
}

func TestRunProjectCreatePrintsSites(t *testing.T) {
	setupTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/u/project":
			_, _ = w.Write([]byte(`{"id":"project-9"}`))
		case "/api/u/project_list":
			_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"project-9","name":"My Site","site_list":[{"id":"site-9","name":"main"}]}]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runProjectCreate(context.Background(), []string{"--name=My Site", "--tpl_id=12"})
	})
	if err != nil {
		t.Fatalf("runProjectCreate: %v", err)
	}
	for _, want := range []string{"Created project project-9", "project-9/site-9\tmain"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q:\n%s", want, output)
		}
	}
}
