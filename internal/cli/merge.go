package cli

import (
	"fmt"
	"strings"
)

const (
	conflictMarkerLocal  = "<<<<<<< local"
	conflictMarkerSep    = "======="
	conflictMarkerRemote = ">>>>>>> remote"
)

// merge3 performs a line-level three-way merge of local and remote against
// their common base. Non-overlapping changes merge cleanly; overlapping
// changes produce git-style conflict markers and clean=false.
func merge3(base string, local string, remote string) (merged string, clean bool) {
	baseLines := strings.Split(base, "\n")
	localLines := strings.Split(local, "\n")
	remoteLines := strings.Split(remote, "\n")
	matchLocal := lcsMatch(baseLines, localLines)
	matchRemote := lcsMatch(baseLines, remoteLines)

	var out []string
	clean = true
	i0, l0, r0 := 0, 0, 0
	// flush merges the unstable region before an anchor: base[i0:bEnd],
	// local[l0:lEnd], remote[r0:rEnd].
	flush := func(bEnd, lEnd, rEnd int) {
		baseChunk := baseLines[i0:bEnd]
		localChunk := localLines[l0:lEnd]
		remoteChunk := remoteLines[r0:rEnd]
		switch {
		case eqLines(localChunk, baseChunk):
			out = append(out, remoteChunk...)
		case eqLines(remoteChunk, baseChunk):
			out = append(out, localChunk...)
		case eqLines(localChunk, remoteChunk):
			out = append(out, localChunk...)
		default:
			clean = false
			out = append(out, conflictMarkerLocal)
			out = append(out, localChunk...)
			out = append(out, conflictMarkerSep)
			out = append(out, remoteChunk...)
			out = append(out, conflictMarkerRemote)
		}
	}

	// Anchors are base lines present unchanged in both local and remote; the
	// LCS alignments are monotonic, so walking them in base order is safe.
	for i := 0; i < len(baseLines); i++ {
		li, lok := matchLocal[i]
		ri, rok := matchRemote[i]
		if !lok || !rok {
			continue
		}
		flush(i, li, ri)
		out = append(out, baseLines[i])
		i0, l0, r0 = i+1, li+1, ri+1
	}
	flush(len(baseLines), len(localLines), len(remoteLines))
	return strings.Join(out, "\n"), clean
}

func eqLines(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// lcsMatch returns the LCS alignment of a onto b as a map from a-line-index
// to b-line-index. Common prefix and suffix lines are matched directly so the
// O(n*m) DP only runs on the changed middle — a long file with one edit costs
// almost nothing.
func lcsMatch(a []string, b []string) map[int]int {
	match := map[int]int{}
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		match[prefix] = prefix
		prefix++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > prefix && bEnd > prefix && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
		match[aEnd] = bEnd
	}

	n, m := aEnd-prefix, bEnd-prefix
	if n == 0 || m == 0 {
		return match
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[prefix+i] == b[prefix+j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[prefix+i] == b[prefix+j]:
			match[prefix+i] = prefix + j
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return match
}

// conflictBlock is one marker-delimited conflict region inside a file body.
type conflictBlock struct {
	start int // line index of "<<<<<<< local"
	sep   int // line index of "======="
	end   int // line index of ">>>>>>> remote"
}

// parseConflictBlocks finds complete, well-formed conflict marker blocks.
// Marker labels after "<<<<<<< " / ">>>>>>> " are not required to be
// local/remote so hand-tweaked markers still parse.
func parseConflictBlocks(lines []string) []conflictBlock {
	var blocks []conflictBlock
	cur := conflictBlock{start: -1, sep: -1}
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "<<<<<<< "):
			cur = conflictBlock{start: i, sep: -1}
		case line == conflictMarkerSep && cur.start >= 0 && cur.sep < 0:
			cur.sep = i
		case strings.HasPrefix(line, ">>>>>>> ") && cur.start >= 0 && cur.sep >= 0:
			cur.end = i
			blocks = append(blocks, cur)
			cur = conflictBlock{start: -1, sep: -1}
		}
	}
	return blocks
}

func hasConflictMarkers(body string) bool {
	return len(parseConflictBlocks(strings.Split(body, "\n"))) > 0
}

// resolveConflictBody rewrites a marker-laden body keeping the local side
// (keepLocal) or the remote side of every conflict block.
func resolveConflictBody(body string, keepLocal bool) (string, int, error) {
	lines := strings.Split(body, "\n")
	blocks := parseConflictBlocks(lines)
	if len(blocks) == 0 {
		return "", 0, fmt.Errorf("no conflict markers found")
	}

	var out []string
	next := 0
	for _, block := range blocks {
		out = append(out, lines[next:block.start]...)
		if keepLocal {
			out = append(out, lines[block.start+1:block.sep]...)
		} else {
			out = append(out, lines[block.sep+1:block.end]...)
		}
		next = block.end + 1
	}
	out = append(out, lines[next:]...)
	return strings.Join(out, "\n"), len(blocks), nil
}
