package projectstate

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestMarkdownLinesCanonicalAndNoSyntheticTerminal(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []string
	}{
		{name: "empty body is one empty line", raw: "", want: []string{"\n"}},
		{name: "single line", raw: "alpha\n", want: []string{"alpha\n"}},
		{name: "mixed line endings", raw: "alpha\r\nbeta\rgamma", want: []string{"alpha\n", "beta\n", "gamma\n"}},
		{name: "trailing blank lines canonicalize away", raw: "alpha\n\n\n", want: []string{"alpha\n"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := state.CanonicalMarkdown([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			lines, err := markdownLines(canonical)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(lines))
			for index := range lines {
				got[index] = string(lines[index])
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("markdownLines(%q) = %#v, want %#v", canonical, got, test.want)
			}
			if joined := bytes.Join(lines, nil); !bytes.Equal(joined, canonical) {
				t.Fatalf("joined lines = %q, want canonical %q", joined, canonical)
			}
		})
	}
}

func TestMarkdownHunksDeletionFirstOracleVectors(t *testing.T) {
	tests := []struct {
		name       string
		base, side string
		want       []markdownHunk
	}{
		{
			name: "match first repeated line",
			base: "A\nA\n", side: "A\n",
			want: []markdownHunk{{Start: 1, End: 2}},
		},
		{
			name: "delete before equal-cost insertion",
			base: "A\nA\n", side: "B\nA\n",
			want: []markdownHunk{{Start: 0, End: 1, Insert: []byte("B\n")}},
		},
		{
			name: "crossed anchors",
			base: "A\nB\n", side: "B\nA\n",
			want: []markdownHunk{{Start: 0, End: 1}, {Start: 2, End: 2, Insert: []byte("A\n")}},
		},
		{
			name: "repeated old-base anchor",
			base: "x\na\nx\n", side: "x\nx\na\n",
			want: []markdownHunk{{Start: 1, End: 2}, {Start: 3, End: 3, Insert: []byte("a\n")}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := markdownHunks(mustMarkdownLines(t, test.base), mustMarkdownLines(t, test.side))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("markdownHunks() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestMarkdownCheckpointReconstructionMatchesFullDPOracle(t *testing.T) {
	sequences := binaryMarkdownSequences(5)
	plans := []markdownDPPlan{
		{RowsAreBase: true, BlockSize: 1},
		{RowsAreBase: true, BlockSize: 2},
		{RowsAreBase: true, BlockSize: 3},
		{RowsAreBase: true, BlockSize: 8},
		{RowsAreBase: false, BlockSize: 1},
		{RowsAreBase: false, BlockSize: 2},
		{RowsAreBase: false, BlockSize: 3},
		{RowsAreBase: false, BlockSize: 8},
	}
	for baseIndex, base := range sequences {
		for sideIndex, side := range sequences {
			want := fullDPMarkdownHunks(base, side)
			for _, plan := range plans {
				got, err := markdownHunksWithPlan(base, side, plan)
				if err != nil {
					t.Fatalf("base=%d side=%d plan=%+v: %v", baseIndex, sideIndex, plan, err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("base=%q side=%q plan=%+v got=%#v want=%#v", joinMarkdownLines(base), joinMarkdownLines(side), plan, got, want)
				}
				patched := applyMarkdownHunksForTest(t, base, got)
				if target := bytes.Join(side, nil); !bytes.Equal(patched, target) {
					t.Fatalf("base=%q side=%q plan=%+v hunks=%#v patched=%q want=%q", joinMarkdownLines(base), joinMarkdownLines(side), plan, got, patched, target)
				}
			}
		}
	}
}

func TestMergeMarkdownCanonicalFastPathsAndDeepCopy(t *testing.T) {
	base := []byte("alpha\r\nbeta")
	ours := []byte("alpha\rB\r")
	theirs := []byte("alpha\nbeta\n")

	got, conflict, err := mergeMarkdown(base, ours, theirs)
	if err != nil || conflict || string(got) != "alpha\nB\n" {
		t.Fatalf("mergeMarkdown() = %q, conflict=%v, err=%v", got, conflict, err)
	}
	got[0] = 'X'
	if string(ours) != "alpha\rB\r" || string(base) != "alpha\r\nbeta" || string(theirs) != "alpha\nbeta\n" {
		t.Fatal("mergeMarkdown result aliases an input")
	}

	equal := []byte("same\r\n")
	got, conflict, err = mergeMarkdown([]byte("base\n"), equal, equal)
	if err != nil || conflict || string(got) != "same\n" {
		t.Fatalf("equal fast path = %q, conflict=%v, err=%v", got, conflict, err)
	}
	got[0] = 'X'
	if string(equal) != "same\r\n" {
		t.Fatal("equal fast path aliases its input")
	}
}

func TestMergeMarkdownNonOverlappingAndBoundaryHunks(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs string
		want               string
	}{
		{
			name: "non-overlapping replacements",
			base: "a\nb\nc\nd\n", ours: "a\nB\nc\nd\n", theirs: "a\nb\nc\nD\n",
			want: "a\nB\nc\nD\n",
		},
		{
			name: "identical shared-anchor insertion coalesces alongside disjoint edits",
			base: "a\nb\nc\nd\ne\n", ours: "a\nb\nx\nc\nd\nE\n", theirs: "A\nb\nx\nc\nd\ne\n",
			want: "A\nb\nx\nc\nd\nE\n",
		},
		{
			name: "start-boundary insertion precedes replacement",
			base: "a\nb\nc\n", ours: "a\nx\nb\nc\n", theirs: "a\nB\nc\n",
			want: "a\nx\nB\nc\n",
		},
		{
			name: "end-boundary insertion follows replacement",
			base: "a\nb\nc\n", ours: "a\nB\nc\n", theirs: "a\nb\nx\nc\n",
			want: "a\nB\nx\nc\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, conflict, err := mergeMarkdown([]byte(test.base), []byte(test.ours), []byte(test.theirs))
			if err != nil || conflict || string(got) != test.want {
				t.Fatalf("mergeMarkdown() = %q, conflict=%v, err=%v; want %q", got, conflict, err, test.want)
			}
		})
	}
}

func TestMergeMarkdownConflictsWithoutMarkers(t *testing.T) {
	tests := []struct {
		name               string
		base, ours, theirs string
	}{
		{name: "unequal shared-anchor insertions", base: "a\nb\n", ours: "a\nx\nb\n", theirs: "a\ny\nb\n"},
		{name: "overlapping replacement and deletion", base: "a\nb\nc\n", ours: "a\nB\nc\n", theirs: "a\nc\n"},
		{name: "insertion strictly inside replacement", base: "a\nb\nc\nd\n", ours: "a\nX\nd\n", theirs: "a\nb\ny\nc\nd\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, conflict, err := mergeMarkdown([]byte(test.base), []byte(test.ours), []byte(test.theirs))
			if err != nil || !conflict || got != nil {
				t.Fatalf("mergeMarkdown() = %q, conflict=%v, err=%v; want nil conflict", got, conflict, err)
			}
			if bytes.Contains(got, []byte("<<<<<<<")) || bytes.Contains(got, []byte(">>>>>>>")) {
				t.Fatalf("mergeMarkdown synthesized conflict markers: %q", got)
			}
		})
	}
}

func TestMarkdownMergeLimitAndFastPathOrdering(t *testing.T) {
	if _, err := markdownCellCount(10_000, 10_000); err != nil {
		t.Fatalf("exact limit rejected: %v", err)
	}
	if _, err := markdownCellCount(10_001, 10_000); !errors.Is(err, ErrMarkdownMergeLimit) {
		t.Fatalf("over-limit error = %v, want ErrMarkdownMergeLimit", err)
	}

	base := repeatedMarkdown("base", 10_001)
	ours := repeatedMarkdown("ours", 10_000)
	theirs := repeatedMarkdown("theirs", 10_000)
	if got, conflict, err := mergeMarkdown(base, base, theirs); err != nil || conflict || !bytes.Equal(got, theirs) {
		t.Fatalf("one-sided over-grid fast path = %d bytes, conflict=%v, err=%v", len(got), conflict, err)
	}
	if got, conflict, err := mergeMarkdown(base, ours, ours); err != nil || conflict || !bytes.Equal(got, ours) {
		t.Fatalf("equal over-grid fast path = %d bytes, conflict=%v, err=%v", len(got), conflict, err)
	}
	if got, conflict, err := mergeMarkdown(base, ours, theirs); !errors.Is(err, ErrMarkdownMergeLimit) || conflict || got != nil {
		t.Fatalf("over-grid merge = %q, conflict=%v, err=%v", got, conflict, err)
	}
}

func TestMarkdownArithmeticGuards(t *testing.T) {
	root := ceilSquareRoot(maxInt())
	if root <= 0 || root < maxInt()/root || (root-1) >= maxInt()/(root-1) {
		t.Fatalf("ceilSquareRoot(maxInt) = %d", root)
	}
	if _, err := markdownCellCount(maxInt(), maxInt()); !errors.Is(err, ErrMarkdownMergeLimit) {
		t.Fatalf("overflowing cell count error = %v, want ErrMarkdownMergeLimit", err)
	}
	if plan := chooseMarkdownDPPlan(100, 1); !plan.RowsAreBase {
		t.Fatalf("chooseMarkdownDPPlan(100,1) = %+v, want base rows", plan)
	}
	if plan := chooseMarkdownDPPlan(1, 100); plan.RowsAreBase {
		t.Fatalf("chooseMarkdownDPPlan(1,100) = %+v, want side rows", plan)
	}
	if plan := chooseMarkdownDPPlan(100, 100); !plan.RowsAreBase {
		t.Fatalf("equal orientation tie = %+v, want base rows", plan)
	}
}

func TestMergeMarkdownDeterministicRepeatedRuns(t *testing.T) {
	base := []byte("x\na\nx\nend\n")
	ours := []byte("x\nx\na\nend\n")
	theirs := []byte("x\na\nx\nend\nextra\n")
	want, wantConflict, err := mergeMarkdown(base, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 50; iteration++ {
		got, conflict, err := mergeMarkdown(base, ours, theirs)
		if err != nil || conflict != wantConflict || !bytes.Equal(got, want) {
			t.Fatalf("iteration %d = %q conflict=%v err=%v; want %q conflict=%v", iteration, got, conflict, err, want, wantConflict)
		}
	}
}

func mustMarkdownLines(t *testing.T, raw string) [][]byte {
	t.Helper()
	canonical, err := state.CanonicalMarkdown([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	lines, err := markdownLines(canonical)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func binaryMarkdownSequences(maxLength int) [][][]byte {
	sequences := [][][]byte{{}}
	level := [][][]byte{{}}
	for length := 1; length <= maxLength; length++ {
		next := make([][][]byte, 0, len(level)*2)
		for _, prefix := range level {
			for _, value := range []string{"A\n", "B\n"} {
				sequence := make([][]byte, len(prefix), len(prefix)+1)
				copy(sequence, prefix)
				sequence = append(sequence, []byte(value))
				next = append(next, sequence)
			}
		}
		sequences = append(sequences, next...)
		level = next
	}
	return sequences
}

func fullDPMarkdownHunks(base, side [][]byte) []markdownHunk {
	n, m := len(base), len(side)
	distance := make([][]int, n+1)
	for index := range distance {
		distance[index] = make([]int, m+1)
	}
	for index := n; index >= 0; index-- {
		for sideIndex := m; sideIndex >= 0; sideIndex-- {
			switch {
			case index == n:
				distance[index][sideIndex] = m - sideIndex
			case sideIndex == m:
				distance[index][sideIndex] = n - index
			case bytes.Equal(base[index], side[sideIndex]):
				distance[index][sideIndex] = distance[index+1][sideIndex+1]
			case distance[index+1][sideIndex] <= distance[index][sideIndex+1]:
				distance[index][sideIndex] = 1 + distance[index+1][sideIndex]
			default:
				distance[index][sideIndex] = 1 + distance[index][sideIndex+1]
			}
		}
	}

	builder := markdownHunkBuilder{}
	baseIndex, sideIndex := 0, 0
	for baseIndex < n || sideIndex < m {
		switch {
		case baseIndex < n && sideIndex < m && bytes.Equal(base[baseIndex], side[sideIndex]):
			builder.match()
			baseIndex++
			sideIndex++
		case baseIndex < n && (sideIndex == m || distance[baseIndex+1][sideIndex] <= distance[baseIndex][sideIndex+1]):
			builder.delete(baseIndex)
			baseIndex++
		default:
			builder.insert(baseIndex, side[sideIndex])
			sideIndex++
		}
	}
	return builder.finish()
}

func joinMarkdownLines(lines [][]byte) string {
	return string(bytes.Join(lines, nil))
}

func applyMarkdownHunksForTest(t *testing.T, base [][]byte, hunks []markdownHunk) []byte {
	t.Helper()
	var patched []byte
	cursor := 0
	for hunkIndex, hunk := range hunks {
		if hunk.Start < cursor || hunk.Start > hunk.End || hunk.End > len(base) {
			t.Fatalf("hunk %d has invalid or overlapping range [%d,%d) after base cursor %d of %d", hunkIndex, hunk.Start, hunk.End, cursor, len(base))
		}
		for baseIndex := cursor; baseIndex < hunk.Start; baseIndex++ {
			patched = append(patched, base[baseIndex]...)
		}
		patched = append(patched, hunk.Insert...)
		cursor = hunk.End
	}
	for baseIndex := cursor; baseIndex < len(base); baseIndex++ {
		patched = append(patched, base[baseIndex]...)
	}
	return patched
}

func repeatedMarkdown(line string, count int) []byte {
	return []byte(strings.Repeat(line+"\n", count))
}
