package projectstate

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"

	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

const markdownMergeCellLimit uint64 = 100_000_000

var ErrMarkdownMergeLimit = errors.New("projectstate: Markdown merge limit exceeded")

type markdownHunk struct {
	Start  int
	End    int
	Insert []byte
}

type markdownDPPlan struct {
	RowsAreBase bool
	BlockSize   int
}

type markdownStep uint8

const (
	markdownStepMatch markdownStep = iota
	markdownStepDelete
	markdownStepInsert
)

func mergeMarkdown(baseRaw, oursRaw, theirsRaw []byte) ([]byte, bool, error) {
	base, err := state.CanonicalMarkdown(baseRaw)
	if err != nil {
		return nil, false, err
	}
	ours, err := state.CanonicalMarkdown(oursRaw)
	if err != nil {
		return nil, false, err
	}
	theirs, err := state.CanonicalMarkdown(theirsRaw)
	if err != nil {
		return nil, false, err
	}
	switch {
	case bytes.Equal(ours, theirs):
		return bytes.Clone(ours), false, nil
	case bytes.Equal(ours, base):
		return bytes.Clone(theirs), false, nil
	case bytes.Equal(theirs, base):
		return bytes.Clone(ours), false, nil
	}

	baseLines, err := markdownLines(base)
	if err != nil {
		return nil, false, err
	}
	oursLines, err := markdownLines(ours)
	if err != nil {
		return nil, false, err
	}
	theirsLines, err := markdownLines(theirs)
	if err != nil {
		return nil, false, err
	}
	oursHunks, err := markdownHunks(baseLines, oursLines)
	if err != nil {
		return nil, false, err
	}
	theirsHunks, err := markdownHunks(baseLines, theirsLines)
	if err != nil {
		return nil, false, err
	}
	combined, conflict, err := combineMarkdownHunks(oursHunks, theirsHunks)
	if err != nil {
		return nil, false, err
	}
	if conflict {
		return nil, true, nil
	}
	merged, err := applyMarkdownHunks(baseLines, combined)
	if err != nil {
		return nil, false, err
	}
	return merged, false, nil
}

func markdownLines(canonical []byte) ([][]byte, error) {
	verified, err := state.CanonicalMarkdown(canonical)
	if err != nil || !bytes.Equal(verified, canonical) {
		return nil, fmt.Errorf("projectstate: Markdown lines require canonical input")
	}
	parts := bytes.SplitAfter(canonical, []byte{'\n'})
	if len(parts) < 2 || len(parts[len(parts)-1]) != 0 {
		return nil, fmt.Errorf("projectstate: canonical Markdown is missing its final line ending")
	}
	return parts[:len(parts)-1], nil
}

func markdownHunks(base, side [][]byte) ([]markdownHunk, error) {
	return markdownHunksWithPlan(base, side, chooseMarkdownDPPlan(len(base), len(side)))
}

func markdownHunksWithPlan(base, side [][]byte, plan markdownDPPlan) ([]markdownHunk, error) {
	if markdownLineSlicesEqual(base, side) {
		return make([]markdownHunk, 0), nil
	}
	prefix := 0
	for prefix < len(base) && prefix < len(side) && bytes.Equal(base[prefix], side[prefix]) {
		prefix++
	}
	base = base[prefix:]
	side = side[prefix:]
	if len(base) == 0 {
		builder := markdownHunkBuilder{}
		for _, line := range side {
			builder.insert(prefix, line)
		}
		return builder.finish(), nil
	}
	if len(side) == 0 {
		builder := markdownHunkBuilder{}
		for index := range base {
			builder.delete(prefix + index)
		}
		return builder.finish(), nil
	}
	if _, err := markdownCellCount(len(base), len(side)); err != nil {
		return nil, err
	}
	if plan.BlockSize <= 0 {
		return nil, fmt.Errorf("projectstate: invalid Markdown DP block size")
	}

	grid := markdownDPGrid{base: base, side: side, rowsAreBase: plan.RowsAreBase}
	if plan.BlockSize > grid.rows() {
		plan.BlockSize = grid.rows()
	}
	checkpoints, boundaries, err := grid.checkpoints(plan.BlockSize)
	if err != nil {
		return nil, err
	}
	builder := markdownHunkBuilder{}
	baseIndex, sideIndex := 0, 0
	for block := 0; block+1 < len(boundaries); block++ {
		start, end := boundaries[block], boundaries[block+1]
		directions, err := grid.blockDirections(start, end, checkpoints[block+1], checkpoints[block])
		if err != nil {
			return nil, err
		}
		for grid.rowCoordinate(baseIndex, sideIndex) < end {
			row := grid.rowCoordinate(baseIndex, sideIndex)
			column := grid.columnCoordinate(baseIndex, sideIndex)
			step, err := directions.get((row-start)*grid.width() + column)
			if err != nil {
				return nil, err
			}
			switch step {
			case markdownStepMatch:
				builder.match()
				baseIndex++
				sideIndex++
			case markdownStepDelete:
				builder.delete(prefix + baseIndex)
				baseIndex++
			case markdownStepInsert:
				builder.insert(prefix+baseIndex, side[sideIndex])
				sideIndex++
			default:
				return nil, fmt.Errorf("projectstate: invalid Markdown edit step")
			}
		}
	}
	for baseIndex < len(base) {
		builder.delete(prefix + baseIndex)
		baseIndex++
	}
	for sideIndex < len(side) {
		builder.insert(prefix+baseIndex, side[sideIndex])
		sideIndex++
	}
	hunks := builder.finish()
	sort.Slice(hunks, func(i, j int) bool { return markdownHunkLess(hunks[i], hunks[j]) })
	return hunks, nil
}

func markdownCellCount(baseLines, sideLines int) (uint64, error) {
	if baseLines < 0 || sideLines < 0 {
		return 0, fmt.Errorf("projectstate: invalid Markdown line count")
	}
	base, side := uint64(baseLines), uint64(sideLines)
	if side != 0 && base > math.MaxUint64/side {
		return 0, ErrMarkdownMergeLimit
	}
	cells := base * side
	if cells > markdownMergeCellLimit {
		return 0, ErrMarkdownMergeLimit
	}
	if base > math.MaxUint32 || side > math.MaxUint32 || base+side > math.MaxUint32 {
		return 0, ErrMarkdownMergeLimit
	}
	return cells, nil
}

func chooseMarkdownDPPlan(baseLines, sideLines int) markdownDPPlan {
	baseRoot := ceilSquareRoot(baseLines)
	sideRoot := ceilSquareRoot(sideLines)
	baseWidth, baseWidthOK := checkedIntAddOne(sideLines)
	sideWidth, sideWidthOK := checkedIntAddOne(baseLines)
	baseRowsEstimate, baseOK := checkedUint64Product(uint64(baseWidth), uint64(baseRoot))
	sideRowsEstimate, sideOK := checkedUint64Product(uint64(sideWidth), uint64(sideRoot))
	baseOK = baseOK && baseWidthOK
	sideOK = sideOK && sideWidthOK
	rowsAreBase := true
	if sideOK && (!baseOK || sideRowsEstimate < baseRowsEstimate) {
		rowsAreBase = false
	}
	rows := baseLines
	if !rowsAreBase {
		rows = sideLines
	}
	root := ceilSquareRoot(rows)
	blockSize := rows
	if root <= maxInt()/4 && root*4 < blockSize {
		blockSize = root * 4
	}
	if blockSize < 1 {
		blockSize = 1
	}
	return markdownDPPlan{RowsAreBase: rowsAreBase, BlockSize: blockSize}
}

type markdownDPGrid struct {
	base, side  [][]byte
	rowsAreBase bool
}

func (g markdownDPGrid) rows() int {
	if g.rowsAreBase {
		return len(g.base)
	}
	return len(g.side)
}

func (g markdownDPGrid) columns() int {
	if g.rowsAreBase {
		return len(g.side)
	}
	return len(g.base)
}

func (g markdownDPGrid) width() int { return g.columns() + 1 }

func (g markdownDPGrid) rowCoordinate(baseIndex, sideIndex int) int {
	if g.rowsAreBase {
		return baseIndex
	}
	return sideIndex
}

func (g markdownDPGrid) columnCoordinate(baseIndex, sideIndex int) int {
	if g.rowsAreBase {
		return sideIndex
	}
	return baseIndex
}

func (g markdownDPGrid) equal(row, column int) bool {
	if g.rowsAreBase {
		return bytes.Equal(g.base[row], g.side[column])
	}
	return bytes.Equal(g.base[column], g.side[row])
}

func (g markdownDPGrid) checkpoints(blockSize int) ([][]uint32, []int, error) {
	boundaries, err := markdownBlockBoundaries(g.rows(), blockSize)
	if err != nil {
		return nil, nil, err
	}
	width := g.width()
	checkpointCells, ok := checkedIntProduct(len(boundaries), width)
	if !ok || !checkedAllocationBytes(checkpointCells, 4) || !checkedAllocationBytes(width, 4) {
		return nil, nil, ErrMarkdownMergeLimit
	}
	checkpoints := make([][]uint32, len(boundaries))
	next := make([]uint32, width)
	for column := 0; column < width; column++ {
		next[column] = uint32(g.columns() - column)
	}
	checkpoint := len(boundaries) - 1
	checkpoints[checkpoint] = append([]uint32(nil), next...)
	checkpoint--
	current := make([]uint32, width)
	for row := g.rows() - 1; row >= 0; row-- {
		g.fillCostRow(row, next, current)
		if checkpoint >= 0 && row == boundaries[checkpoint] {
			checkpoints[checkpoint] = append([]uint32(nil), current...)
			checkpoint--
		}
		next, current = current, next
	}
	if checkpoint != -1 {
		return nil, nil, fmt.Errorf("projectstate: incomplete Markdown DP checkpoints")
	}
	return checkpoints, boundaries, nil
}

func (g markdownDPGrid) fillCostRow(row int, next, current []uint32) {
	columns := g.columns()
	current[columns] = uint32(g.rows() - row)
	for column := columns - 1; column >= 0; column-- {
		switch {
		case g.equal(row, column):
			current[column] = next[column+1]
		case g.rowsAreBase && next[column] <= current[column+1]:
			current[column] = 1 + next[column]
		case g.rowsAreBase:
			current[column] = 1 + current[column+1]
		case current[column+1] <= next[column]:
			current[column] = 1 + current[column+1]
		default:
			current[column] = 1 + next[column]
		}
	}
}

func (g markdownDPGrid) blockDirections(start, end int, endCosts, expectedStartCosts []uint32) (packedMarkdownSteps, error) {
	if start < 0 || end <= start || end > g.rows() || len(endCosts) != g.width() || len(expectedStartCosts) != g.width() {
		return packedMarkdownSteps{}, fmt.Errorf("projectstate: invalid Markdown DP block")
	}
	cells, ok := checkedIntProduct(end-start, g.width())
	if !ok || cells > maxInt()-3 {
		return packedMarkdownSteps{}, ErrMarkdownMergeLimit
	}
	directions := newPackedMarkdownSteps(cells)
	next := append([]uint32(nil), endCosts...)
	current := make([]uint32, g.width())
	for row := end - 1; row >= start; row-- {
		g.fillDirectionRow(row, next, current, &directions, (row-start)*g.width())
		next, current = current, next
	}
	if !equalUint32s(next, expectedStartCosts) {
		return packedMarkdownSteps{}, fmt.Errorf("projectstate: Markdown DP checkpoint drift")
	}
	return directions, nil
}

func (g markdownDPGrid) fillDirectionRow(row int, next, current []uint32, directions *packedMarkdownSteps, offset int) {
	columns := g.columns()
	current[columns] = uint32(g.rows() - row)
	if g.rowsAreBase {
		directions.set(offset+columns, markdownStepDelete)
	} else {
		directions.set(offset+columns, markdownStepInsert)
	}
	for column := columns - 1; column >= 0; column-- {
		switch {
		case g.equal(row, column):
			current[column] = next[column+1]
			directions.set(offset+column, markdownStepMatch)
		case g.rowsAreBase && next[column] <= current[column+1]:
			current[column] = 1 + next[column]
			directions.set(offset+column, markdownStepDelete)
		case g.rowsAreBase:
			current[column] = 1 + current[column+1]
			directions.set(offset+column, markdownStepInsert)
		case current[column+1] <= next[column]:
			current[column] = 1 + current[column+1]
			directions.set(offset+column, markdownStepDelete)
		default:
			current[column] = 1 + next[column]
			directions.set(offset+column, markdownStepInsert)
		}
	}
}

type packedMarkdownSteps struct {
	data  []byte
	cells int
}

func newPackedMarkdownSteps(cells int) packedMarkdownSteps {
	return packedMarkdownSteps{data: make([]byte, (cells+3)/4), cells: cells}
}

func (p *packedMarkdownSteps) set(cell int, step markdownStep) {
	index, shift := cell>>2, uint((cell&3)*2)
	p.data[index] = (p.data[index] &^ byte(3<<shift)) | byte(step)<<shift
}

func (p packedMarkdownSteps) get(cell int) (markdownStep, error) {
	if cell < 0 || cell >= p.cells {
		return 0, fmt.Errorf("projectstate: Markdown DP direction out of bounds")
	}
	shift := uint((cell & 3) * 2)
	step := markdownStep((p.data[cell>>2] >> shift) & 3)
	if step > markdownStepInsert {
		return 0, fmt.Errorf("projectstate: invalid packed Markdown edit step")
	}
	return step, nil
}

type markdownHunkBuilder struct {
	hunks   []markdownHunk
	pending markdownHunk
	active  bool
}

func (b *markdownHunkBuilder) match() { b.flush() }

func (b *markdownHunkBuilder) delete(baseIndex int) {
	b.start(baseIndex)
	b.pending.End = baseIndex + 1
}

func (b *markdownHunkBuilder) insert(baseIndex int, line []byte) {
	b.start(baseIndex)
	b.pending.Insert = append(b.pending.Insert, line...)
}

func (b *markdownHunkBuilder) start(baseIndex int) {
	if b.active {
		return
	}
	b.pending = markdownHunk{Start: baseIndex, End: baseIndex}
	b.active = true
}

func (b *markdownHunkBuilder) flush() {
	if !b.active {
		return
	}
	b.hunks = append(b.hunks, b.pending)
	b.pending = markdownHunk{}
	b.active = false
}

func (b *markdownHunkBuilder) finish() []markdownHunk {
	b.flush()
	if b.hunks == nil {
		return make([]markdownHunk, 0)
	}
	return b.hunks
}

func combineMarkdownHunks(ours, theirs []markdownHunk) ([]markdownHunk, bool, error) {
	combined := make([]markdownHunk, 0, len(ours)+len(theirs))
	oursIndex, theirsIndex := 0, 0
	for oursIndex < len(ours) && theirsIndex < len(theirs) {
		oursHunk, theirsHunk := ours[oursIndex], theirs[theirsIndex]
		if !validMarkdownHunk(oursHunk) || !validMarkdownHunk(theirsHunk) {
			return nil, false, fmt.Errorf("projectstate: invalid Markdown hunk")
		}
		switch {
		case markdownHunksEqual(oursHunk, theirsHunk):
			combined = append(combined, cloneMarkdownHunk(oursHunk))
			oursIndex++
			theirsIndex++
		case markdownHunksConflict(oursHunk, theirsHunk):
			return nil, true, nil
		case markdownHunkLess(oursHunk, theirsHunk):
			combined = append(combined, cloneMarkdownHunk(oursHunk))
			oursIndex++
		default:
			combined = append(combined, cloneMarkdownHunk(theirsHunk))
			theirsIndex++
		}
	}
	for ; oursIndex < len(ours); oursIndex++ {
		if !validMarkdownHunk(ours[oursIndex]) {
			return nil, false, fmt.Errorf("projectstate: invalid Markdown hunk")
		}
		combined = append(combined, cloneMarkdownHunk(ours[oursIndex]))
	}
	for ; theirsIndex < len(theirs); theirsIndex++ {
		if !validMarkdownHunk(theirs[theirsIndex]) {
			return nil, false, fmt.Errorf("projectstate: invalid Markdown hunk")
		}
		combined = append(combined, cloneMarkdownHunk(theirs[theirsIndex]))
	}
	sort.Slice(combined, func(i, j int) bool { return markdownHunkLess(combined[i], combined[j]) })
	return combined, false, nil
}

func markdownHunksEqual(left, right markdownHunk) bool {
	return left.Start == right.Start && left.End == right.End && bytes.Equal(left.Insert, right.Insert)
}

func markdownHunksConflict(left, right markdownHunk) bool {
	leftInsertion := left.Start == left.End
	rightInsertion := right.Start == right.End
	switch {
	case leftInsertion && rightInsertion:
		return left.Start == right.Start
	case leftInsertion:
		return right.Start < left.Start && left.Start < right.End
	case rightInsertion:
		return left.Start < right.Start && right.Start < left.End
	default:
		return max(left.Start, right.Start) < min(left.End, right.End)
	}
}

func markdownHunkLess(left, right markdownHunk) bool {
	if left.Start != right.Start {
		return left.Start < right.Start
	}
	if left.End != right.End {
		return left.End < right.End
	}
	return bytes.Compare(left.Insert, right.Insert) < 0
}

func validMarkdownHunk(hunk markdownHunk) bool {
	return hunk.Start >= 0 && hunk.End >= hunk.Start
}

func cloneMarkdownHunk(hunk markdownHunk) markdownHunk {
	hunk.Insert = bytes.Clone(hunk.Insert)
	return hunk
}

func applyMarkdownHunks(base [][]byte, hunks []markdownHunk) ([]byte, error) {
	cursor := 0
	var output []byte
	for _, hunk := range hunks {
		if !validMarkdownHunk(hunk) || hunk.Start < cursor || hunk.End > len(base) {
			return nil, fmt.Errorf("projectstate: invalid or overlapping Markdown hunks")
		}
		for _, line := range base[cursor:hunk.Start] {
			output = append(output, line...)
		}
		output = append(output, hunk.Insert...)
		cursor = hunk.End
	}
	for _, line := range base[cursor:] {
		output = append(output, line...)
	}
	canonical, err := state.CanonicalMarkdown(output)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func markdownLineSlicesEqual(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !bytes.Equal(left[index], right[index]) {
			return false
		}
	}
	return true
}

func markdownBlockBoundaries(rows, blockSize int) ([]int, error) {
	if rows < 0 || blockSize <= 0 {
		return nil, fmt.Errorf("projectstate: invalid Markdown DP dimensions")
	}
	boundaries := []int{0}
	for boundary := blockSize; boundary < rows; {
		boundaries = append(boundaries, boundary)
		if boundary > maxInt()-blockSize {
			return nil, ErrMarkdownMergeLimit
		}
		boundary += blockSize
	}
	if rows != 0 {
		boundaries = append(boundaries, rows)
	}
	return boundaries, nil
}

func ceilSquareRoot(value int) int {
	if value <= 0 {
		return 0
	}
	low, high := 1, value
	for low < high {
		middle := low + (high-low)/2
		quotient := value / middle
		if value%middle != 0 {
			quotient++
		}
		if middle >= quotient {
			high = middle
		} else {
			low = middle + 1
		}
	}
	return low
}

func checkedIntAddOne(value int) (int, bool) {
	if value < 0 || value == maxInt() {
		return 0, false
	}
	return value + 1, true
}

func checkedAllocationBytes(count, elementBytes int) bool {
	_, ok := checkedIntProduct(count, elementBytes)
	return ok
}

func checkedUint64Product(left, right uint64) (uint64, bool) {
	if right != 0 && left > math.MaxUint64/right {
		return 0, false
	}
	return left * right, true
}

func checkedIntProduct(left, right int) (int, bool) {
	if left < 0 || right < 0 || (right != 0 && left > maxInt()/right) {
		return 0, false
	}
	return left * right, true
}

func equalUint32s(left, right []uint32) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func maxInt() int { return int(^uint(0) >> 1) }
