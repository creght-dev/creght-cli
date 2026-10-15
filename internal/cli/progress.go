package cli

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// transferProgress prints a progress report to stderr while a package is
// exported or imported: one summary line ("downloading 119.2 MB 2.1 MB/s
// 0:57") plus, while asset files download in parallel, one line per file in
// flight with its own progress. Packages can be hundreds of MB, so without this
// the command looks stuck. It only prints when stderr is a terminal; pipes and
// CI logs stay clean.
type transferProgress struct {
	label   string
	total   int64 // 0 when unknown
	out     *os.File
	enabled bool

	filesTotal int // >0: also show "12/36 files"
	filesDone  int

	mu      sync.Mutex
	n       int64
	start   time.Time
	last    time.Time
	printed bool
	lines   int // lines drawn last time, to redraw in place

	active map[int]*fileProgress // files downloading now, by index
}

type fileProgress struct {
	name  string
	n     int64
	total int64 // 0 when unknown
}

func newTransferProgress(label string, total int64) *transferProgress {
	return &transferProgress{label: label, total: total, out: os.Stderr, enabled: isTerminal(os.Stderr), start: time.Now()}
}

// add counts n more bytes; a negative n takes back bytes of a failed attempt.
func (p *transferProgress) add(n int) {
	if n == 0 {
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

// startFile shows a line for a file that started downloading; addFile counts
// its bytes (they also count toward the total); endFile removes the line.
func (p *transferProgress) startFile(id int, name string, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil {
		p.active = map[int]*fileProgress{}
	}
	p.active[id] = &fileProgress{name: name, total: total}
}

func (p *transferProgress) addFile(id, n int) {
	p.mu.Lock()
	if f := p.active[id]; f != nil {
		f.n += int64(n)
	}
	p.mu.Unlock()
	p.add(n)
}

func (p *transferProgress) endFile(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.active, id)
}

func (p *transferProgress) printLocked() {
	lines := []string{p.summaryLocked()}
	ids := make([]int, 0, len(p.active))
	for id := range p.active {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		f := p.active[id]
		line := fmt.Sprintf("  ↓ %s  %s", shortName(f.name, 48), formatMB(f.n))
		if f.total > 0 {
			total := max(f.total, f.n)
			line += fmt.Sprintf(" / %s (%d%%)", formatMB(total), f.n*100/total)
		}
		lines = append(lines, line)
	}
	// Go back to the first line drawn last time and clear everything below it.
	if p.lines > 1 {
		fmt.Fprintf(p.out, "\033[%dA", p.lines-1)
	}
	fmt.Fprintf(p.out, "\r\033[J%s", strings.Join(lines, "\n"))
	p.lines = len(lines)
	p.printed = true
}

func shortName(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n/2-1]) + "…" + string(r[len(r)-n/2:])
}

func (p *transferProgress) summaryLocked() string {
	elapsed := time.Since(p.start)
	speed := float64(p.n) / elapsed.Seconds()
	line := fmt.Sprintf("%s %s", p.label, formatMB(p.n))
	if p.total > 0 {
		// The total comes from recorded sizes, which can be slightly off: never show more than 100%.
		total := max(p.total, p.n)
		line += fmt.Sprintf(" / %s (%d%%)", formatMB(total), p.n*100/total)
	}
	if p.filesTotal > 0 {
		line += fmt.Sprintf("  %d/%d files", p.filesDone, p.filesTotal)
	}
	line += fmt.Sprintf("  %s/s  %s", formatMB(int64(speed)), formatElapsed(elapsed))
	return line
}

func (p *transferProgress) fileDone() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.filesDone++
	if p.enabled {
		p.printLocked()
	}
}

// done clears the progress lines so the final summary starts on a clean line.
func (p *transferProgress) done() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.enabled && p.printed {
		if p.lines > 1 {
			fmt.Fprintf(p.out, "\033[%dA", p.lines-1)
		}
		fmt.Fprint(p.out, "\r\033[J")
		p.lines = 0
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
	r       io.Reader
	p       *transferProgress
	counted *int64 // optional: bytes this reader has reported
	fileID  *int   // optional: also count toward this file's line
}

func (r progressReader) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if r.fileID != nil {
		r.p.addFile(*r.fileID, n)
	} else {
		r.p.add(n)
	}
	if r.counted != nil {
		*r.counted += int64(n)
	}
	return n, err
}
