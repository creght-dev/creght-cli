package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// transferProgress prints a one-line progress report ("downloading 119.2 MB
// 2.1 MB/s 0:57") to stderr while a package is exported or imported. Packages
// can be hundreds of MB and the server streams them without a total size, so
// without this the command looks stuck. It only prints when stderr is a
// terminal; pipes and CI logs stay clean.
type transferProgress struct {
	label   string
	total   int64 // 0 when unknown
	out     *os.File
	enabled bool

	mu      sync.Mutex
	n       int64
	start   time.Time
	last    time.Time
	printed bool
}

func newTransferProgress(label string, total int64) *transferProgress {
	return &transferProgress{label: label, total: total, out: os.Stderr, enabled: isTerminal(os.Stderr), start: time.Now()}
}

func (p *transferProgress) add(n int) {
	if n <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n += int64(n)
	if p.enabled && time.Since(p.last) >= 300*time.Millisecond {
		p.last = time.Now()
		p.printLocked()
	}
}

func (p *transferProgress) printLocked() {
	elapsed := time.Since(p.start)
	speed := float64(p.n) / elapsed.Seconds()
	line := fmt.Sprintf("%s %s", p.label, formatMB(p.n))
	if p.total > 0 {
		line += fmt.Sprintf(" / %s (%d%%)", formatMB(p.total), p.n*100/p.total)
	}
	line += fmt.Sprintf("  %s/s  %s", formatMB(int64(speed)), formatElapsed(elapsed))
	fmt.Fprintf(p.out, "\r\033[K%s", line)
	p.printed = true
}

// done clears the progress line so the final summary starts on a clean line.
func (p *transferProgress) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled && p.printed {
		fmt.Fprint(p.out, "\r\033[K")
	}
}

func formatMB(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
}

func formatElapsed(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

type progressWriter struct {
	w io.Writer
	p *transferProgress
}

func (w progressWriter) Write(b []byte) (int, error) {
	n, err := w.w.Write(b)
	w.p.add(n)
	return n, err
}

type progressReader struct {
	r io.Reader
	p *transferProgress
}

func (r progressReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	r.p.add(n)
	return n, err
}
