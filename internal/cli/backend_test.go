package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTableRecordCreateResolvesTableKey(t *testing.T) {
	setupTestConfig(t)

	dataPath := filepath.Join(t.TempDir(), "record.json")
	if err := os.WriteFile(dataPath, []byte(`{"name":"Ada","status":"confirmed"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotRecord map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/p/project/project-1/table_list":
			_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"table-1","key":"appointments","name":"Appointments"}]}`))
		case "/api/p/project/project-1/table/table-1/record":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotRecord); err != nil {
				t.Fatalf("decode record: %v", err)
			}
			_, _ = w.Write([]byte(`{"id":"record-1"}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	var err error
	output := captureStdout(t, func() {
		err = runTableRecordCreate(context.Background(), []string{
			"--site_id=project-1/site-1",
			"--table=appointments",
			"--data=" + dataPath,
			"--sort=42",
		})
	})
	if err != nil {
		t.Fatalf("runTableRecordCreate: %v", err)
	}
	if !strings.Contains(output, "record-1") {
		t.Fatalf("output = %q", output)
	}
	if gotRecord["sort"] != float64(42) {
		t.Fatalf("sort = %#v, want 42", gotRecord["sort"])
	}
	body := gotRecord["body"].(map[string]any)
	if body["name"] != "Ada" || body["status"] != "confirmed" {
		t.Fatalf("body = %#v", body)
	}
}

func TestRunFuncRunSendsInvokePayload(t *testing.T) {
	setupTestConfig(t)

	inputPath := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(inputPath, []byte(`{"date":"2026-07-05"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/p/project/project-1/func/run" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"ok":true}}`))
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	err := runFuncRun(context.Background(), []string{
		"--site_id=project-1/site-1",
		"--key=booking.create",
		"--input=" + inputPath,
		"--timeout_ms=3000",
	})
	if err != nil {
		t.Fatalf("runFuncRun: %v", err)
	}
	if gotBody["key"] != "booking.create" {
		t.Fatalf("key = %#v", gotBody["key"])
	}
	if gotBody["site_id"] != "site-1" {
		t.Fatalf("site_id = %#v", gotBody["site_id"])
	}
	if gotBody["timeout_ms"] != float64(3000) {
		t.Fatalf("timeout_ms = %#v", gotBody["timeout_ms"])
	}
	input := gotBody["input"].(map[string]any)
	if input["date"] != "2026-07-05" {
		t.Fatalf("input = %#v", input)
	}
}

func TestRunFuncManagementCommandsAreRemoved(t *testing.T) {
	err := runFunc(context.Background(), []string{"create", "--site_id=project-1/site-1", "--key=booking"})
	if err == nil || !strings.Contains(err.Error(), "unknown func command: create") {
		t.Fatalf("runFunc create err = %v, want unknown command", err)
	}

	output := captureStdout(t, func() {
		err = runFunc(context.Background(), []string{"help"})
	})
	if err != nil {
		t.Fatalf("runFunc help: %v", err)
	}
	if !strings.Contains(output, "creght func run") {
		t.Fatalf("help output missing func run: %q", output)
	}
	for _, removed := range []string{
		"creght func list",
		"creght func get",
		"creght func create",
		"creght func update",
		"creght func delete",
	} {
		if strings.Contains(output, removed) {
			t.Fatalf("help output contains removed command %q: %q", removed, output)
		}
	}
}

func TestNormalizeTableRecordFilterAcceptsToolFieldNames(t *testing.T) {
	filter := normalizeTableRecordFilter(map[string]any{
		"match": "and",
		"conditions": []any{
			map[string]any{
				"field_id": "status",
				"operator": "in",
				"values":   []any{"confirmed", "pending"},
			},
		},
	})

	conditions := filter["conditions"].([]any)
	condition := conditions[0].(map[string]any)
	if condition["fieldId"] != "status" {
		t.Fatalf("fieldId = %#v", condition["fieldId"])
	}
	values := condition["value"].([]any)
	if values[0] != "confirmed" || values[1] != "pending" {
		t.Fatalf("value = %#v", values)
	}
}

func TestOutputJSONWritesFile(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "nested", "item.json")
	output := captureStdout(t, func() {
		if err := outputJSON(map[string]any{"ok": true}, outPath); err != nil {
			t.Fatalf("outputJSON: %v", err)
		}
	})
	if !strings.Contains(output, "Wrote "+outPath) {
		t.Fatalf("output = %q", output)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "{\n  \"ok\": true\n}\n" {
		t.Fatalf("body = %q", string(body))
	}
}

func setupTestConfig(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfgPath, err := configPath()
	if err != nil {
		t.Fatalf("configPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"token":"test-token"}`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}
