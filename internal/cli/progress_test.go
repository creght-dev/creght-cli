package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProgressWriterCountsAndPrints(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := newTransferProgress("downloading", 4<<20)
	p.out, p.enabled = w, true
	var buf bytes.Buffer
	pw := progressWriter{w: &buf, p: p}
	if _, err := pw.Write(make([]byte, 2<<20)); err != nil {
		t.Fatal(err)
	}
	p.done()
	w.Close()
	out, _ := readAll(r)
	if buf.Len() != 2<<20 || p.n != 2<<20 {
		t.Fatalf("wrote %d, counted %d", buf.Len(), p.n)
	}
	if !strings.Contains(out, "downloading 2.0 MB / 4.0 MB (50%)") {
		t.Fatalf("unexpected progress output %q", out)
	}
}

func TestProgressDisabledPrintsNothing(t *testing.T) {
	r, w, _ := os.Pipe()
	p := newTransferProgress("uploading", 0)
	p.out, p.enabled = w, false
	_, _ = progressReader{r: strings.NewReader("hello"), p: p}.Read(make([]byte, 10))
	p.done()
	w.Close()
	out, _ := readAll(r)
	if out != "" || p.n != 5 {
		t.Fatalf("out=%q n=%d", out, p.n)
	}
}

func TestFormatElapsed(t *testing.T) {
	if got := formatElapsed(83 * time.Second); got != "1:23" {
		t.Fatal(got)
	}
}

func readAll(f *os.File) (string, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(f)
	return b.String(), err
}
