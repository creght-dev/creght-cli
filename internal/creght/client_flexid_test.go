package creght

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// jsonServer replies to every request with the given body, recording the request
// body it received.
func jsonServer(t *testing.T, responseBody string) (*httptest.Server, *string) {
	t.Helper()
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bs, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		got = string(bs)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responseBody))
	}))
	t.Cleanup(server.Close)
	return server, &got
}

// The backend sends user ids as strings because they are snowflake ids too large
// for a JS double. Decoding them into a number type fails the whole response, so
// listing collections — the first thing every cms/form/table command does to
// resolve a --key — used to fail against a current backend.
func TestGetCMSCollectionListAcceptsStringUserID(t *testing.T) {
	server, _ := jsonServer(t, `{"total":1,"list":[{"id":"aB","key":"blog","user_id":"2092286295073099801","name":"Blog"}]}`)
	client := NewClient(server.URL, "token")

	res, err := client.GetCMSCollectionList(context.Background(), "project-1", nil)
	if err != nil {
		t.Fatalf("GetCMSCollectionList: %v", err)
	}
	if len(res.List) != 1 {
		t.Fatalf("list length = %d, want 1", len(res.List))
	}
	// Every digit survives; this is the value a float64 would have rounded to
	// ...800.
	if got := res.List[0].UserID; got != "2092286295073099801" {
		t.Errorf("user_id = %q, want 2092286295073099801", got)
	}
}

// Older backends still send numbers, so one CLI build has to read both.
func TestGetContentListAcceptsNumberUserID(t *testing.T) {
	server, _ := jsonServer(t, `{"total":1,"list":[{"id":"c1","slug":"hello","user_id":155}]}`)
	client := NewClient(server.URL, "token")

	res, err := client.GetContentList(context.Background(), "project-1", "app-1", nil, nil)
	if err != nil {
		t.Fatalf("GetContentList: %v", err)
	}
	if len(res.List) != 1 {
		t.Fatalf("list length = %d, want 1", len(res.List))
	}
	if got := res.List[0].UserID; got != "155" {
		t.Errorf("user_id = %q, want 155", got)
	}
}

// A create request must not carry a user_id the caller never set: the server
// takes the owner from the token, and an empty one would be a 400.
func TestCreateCMSCollectionOmitsEmptyUserID(t *testing.T) {
	server, gotBody := jsonServer(t, `{"id":"aB"}`)
	client := NewClient(server.URL, "token")

	_, err := client.CreateCMSCollection(context.Background(), "project-1", ContentApp{Key: "blog", Name: "Blog"})
	if err != nil {
		t.Fatalf("CreateCMSCollection: %v", err)
	}
	if strings.Contains(*gotBody, "user_id") {
		t.Errorf("request body = %s, want no user_id field", *gotBody)
	}
}

// A value read from the API and sent back stays a decimal string, which the
// backend accepts alongside numbers.
func TestFlexIDMarshalsAsString(t *testing.T) {
	bs, err := json.Marshal(ContentApp{UserID: "2092286295073099801"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(bs), `"user_id":"2092286295073099801"`) {
		t.Errorf("marshalled = %s, want a string user_id", bs)
	}
}

func TestFlexIDUnmarshal(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    FlexID
		wantErr bool
	}{
		{name: "string", input: `"12"`, want: "12"},
		{name: "number", input: `12`, want: "12"},
		{name: "null", input: `null`, want: ""},
		{name: "empty string", input: `""`, want: ""},
		// -1 is the platform's "everyone"/system-owned sentinel.
		{name: "negative", input: `-1`, want: "-1"},
		{name: "bool", input: `true`, wantErr: true},
		{name: "object", input: `{}`, wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got FlexID
			err := json.Unmarshal([]byte(c.input), &got)
			if c.wantErr {
				if err == nil {
					t.Fatalf("Unmarshal(%s) = %q, want an error", c.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", c.input, err)
			}
			if got != c.want {
				t.Errorf("Unmarshal(%s) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}
