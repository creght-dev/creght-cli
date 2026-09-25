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

// Filters the platform used to ignore silently must fail here, with the right
// shape in the message.
func TestValidateTableRecordFilterRejectsMistakes(t *testing.T) {
	for name, tc := range map[string]struct {
		filter string
		want   string
	}{
		"field/op keys":     {`{"conditions":[{"field":"body.channel_id","op":"eq","value":"x"}]}`, `"field" (did you mean "fieldId"?)`},
		"and at top":        {`{"and":[{"fieldId":"a","operator":"eq","value":1}]}`, `"and" (did you mean "conditions"?)`},
		"no conditions":     {`{}`, `missing "conditions"`},
		"conditions object": {`{"conditions":{"fieldId":"a"}}`, `must be an array`},
		"no fieldId":        {`{"conditions":[{"operator":"eq","value":1}]}`, `no "fieldId"`},
		"no operator":       {`{"conditions":[{"fieldId":"a","value":1}]}`, `no "operator"`},
		"bad operator":      {`{"conditions":[{"fieldId":"a","operator":"like","value":"x"}]}`, `unsupported operator "like"`},
		"no value":          {`{"conditions":[{"fieldId":"a","operator":"eq"}]}`, `no "value"`},
		"in not array":      {`{"conditions":[{"fieldId":"a","operator":"in","value":"x"}]}`, `needs an array`},
		"between one value": {`{"conditions":[{"fieldId":"a","operator":"between","value":[1]}]}`, `[from, to]`},
		"match or":          {`{"match":"or","conditions":[{"fieldId":"a","operator":"eq","value":1}]}`, `always AND-ed`},
	} {
		t.Run(name, func(t *testing.T) {
			var filter map[string]any
			if err := json.Unmarshal([]byte(tc.filter), &filter); err != nil {
				t.Fatal(err)
			}
			_, err := validateTableRecordFilter(filter)
			if err == nil {
				t.Fatalf("accepted %s", tc.filter)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), tableFilterExample) {
				t.Fatalf("err = %v, want %q and the example", err, tc.want)
			}
		})
	}
}

func TestValidateTableRecordFilterNormalizes(t *testing.T) {
	var filter map[string]any
	_ = json.Unmarshal([]byte(`{"match":"and","conditions":[
		{"fieldId":"body.channel_id","operator":"=","value":"x"},
		{"field_id":"views","operator":">=","value":100},
		{"fieldId":"date","operator":"between","values":["2026-09-01","2026-09-10"]}]}`), &filter)
	got, err := validateTableRecordFilter(filter)
	if err != nil {
		t.Fatal(err)
	}
	bs, _ := json.Marshal(got)
	want := `{"conditions":[{"fieldId":"channel_id","operator":"eq","value":"x"},{"fieldId":"views","operator":"gte","value":100},{"fieldId":"date","operator":"between","value":["2026-09-01","2026-09-10"]}]}`
	if string(bs) != want {
		t.Fatalf("normalized = %s\nwant       %s", bs, want)
	}
}

func TestReadJSONObjectArgInlineOrFile(t *testing.T) {
	inline, err := readJSONObjectArg("filter", ` {"conditions":[]}`)
	if err != nil || inline["conditions"] == nil {
		t.Fatalf("inline: %v %v", inline, err)
	}
	path := filepath.Join(t.TempDir(), "f.json")
	if err := os.WriteFile(path, []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if file, err := readJSONObjectArg("where", path); err != nil || file["a"] != float64(1) {
		t.Fatalf("file: %v %v", file, err)
	}
	if _, err := readJSONObjectArg("filter", `{"conditions":`); err == nil || !strings.Contains(err.Error(), "--filter") {
		t.Fatalf("broken inline JSON: err = %v", err)
	}
	if _, err := readJSONObjectArg("filter", filepath.Join(t.TempDir(), "missing.json")); err == nil || !strings.Contains(err.Error(), "inline JSON") {
		t.Fatalf("missing file: err = %v", err)
	}
}

func TestNormalizeTableRecordWhere(t *testing.T) {
	got, err := normalizeTableRecordWhere(map[string]any{"body.channel_id": "x", "status": "ok"})
	if err != nil || got["channel_id"] != "x" || got["status"] != "ok" || len(got) != 2 {
		t.Fatalf("where = %v, err = %v", got, err)
	}
	if _, err := normalizeTableRecordWhere(map[string]any{"conditions": []any{}}); err == nil || !strings.Contains(err.Error(), "--filter") {
		t.Fatalf("conditions in --where: err = %v", err)
	}
}

// End to end: an inline --filter reaches the platform already normalized, and
// a wrong one never leaves the machine.
func TestRunTableRecordListSendsValidatedInlineFilter(t *testing.T) {
	var gotBody map[string]any
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/table_list"):
			_, _ = w.Write([]byte(`{"total":1,"list":[{"id":"t1","key":"social_posts"}]}`))
		case strings.HasSuffix(r.URL.Path, "/record_list"):
			requests++
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			_, _ = w.Write([]byte(`{"total":2,"has_more":false,"list":[]}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	t.Setenv("CREGHT_API_HOST", server.URL)

	args := []string{"--site_id=p1/s1", "--table=social_posts", "--limit=3",
		`--filter={"conditions":[{"fieldId":"body.channel_id","operator":"eq","value":"abc"}]}`}
	var err error
	captureStdout(t, func() { err = runTableRecordList(context.Background(), args) })
	if err != nil {
		t.Fatalf("runTableRecordList: %v", err)
	}
	bs, _ := json.Marshal(gotBody["filter"])
	if string(bs) != `{"conditions":[{"fieldId":"channel_id","operator":"eq","value":"abc"}]}` {
		t.Fatalf("sent filter = %s", bs)
	}

	bad := []string{"--site_id=p1/s1", "--table=social_posts",
		`--filter={"conditions":[{"field":"body.channel_id","op":"eq","value":"abc"}]}`}
	captureStdout(t, func() { err = runTableRecordList(context.Background(), bad) })
	if err == nil || !strings.Contains(err.Error(), "fieldId") {
		t.Fatalf("wrong filter: err = %v", err)
	}
	if requests != 1 {
		t.Fatalf("record_list called %d times, want 1 (the wrong filter must not be sent)", requests)
	}
}
