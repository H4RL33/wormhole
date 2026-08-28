package git

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	maximumStoredTreeFiles     = 10_000
	maximumStoredTreeFileBytes = 1 << 20
	maximumStoredTreeDataBytes = 16 << 20
)

var ErrStoredTree = errors.New("git: invalid stored tree")

// EncodeStoredTree preserves the exact canonical ProjectState file bytes in a
// deterministic, length-delimited container. The container is transport and
// database representation only; ProjectState remains the schema authority.
func EncodeStoredTree(tree projectstate.Tree) ([]byte, error) {
	if err := validateStoredTreeBounds(tree, false); err != nil {
		return nil, err
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrStoredTree, err)
	}
	if err := projectstate.Validate(snapshot); err != nil {
		return nil, fmt.Errorf("%w: validate: %v", ErrStoredTree, err)
	}
	if _, err := projectstate.DigestTree(tree); err != nil {
		return nil, fmt.Errorf("%w: digest: %v", ErrStoredTree, err)
	}

	files := cloneStoredTree(tree)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var encoded bytes.Buffer
	if err := binary.Write(&encoded, binary.BigEndian, uint32(len(files))); err != nil {
		return nil, fmt.Errorf("%w: file count: %v", ErrStoredTree, err)
	}
	for _, file := range files {
		if err := binary.Write(&encoded, binary.BigEndian, uint32(len(file.Path))); err != nil {
			return nil, fmt.Errorf("%w: path length: %v", ErrStoredTree, err)
		}
		_, _ = encoded.WriteString(file.Path)
		if err := binary.Write(&encoded, binary.BigEndian, uint64(len(file.Data))); err != nil {
			return nil, fmt.Errorf("%w: data length: %v", ErrStoredTree, err)
		}
		_, _ = encoded.Write(file.Data)
	}
	return encoded.Bytes(), nil
}

// DecodeStoredTree rejects ambiguous or noncanonical containers before asking
// ProjectState to decode, validate, and digest the exact retained file bytes.
func DecodeStoredTree(raw []byte) (projectstate.Tree, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("%w: missing file count", ErrStoredTree)
	}
	fileCount := binary.BigEndian.Uint32(raw[:4])
	if fileCount > maximumStoredTreeFiles {
		return nil, fmt.Errorf("%w: too many files", ErrStoredTree)
	}
	offset := 4
	tree := make(projectstate.Tree, 0, int(fileCount))
	for index := uint32(0); index < fileCount; index++ {
		if len(raw)-offset < 4 {
			return nil, fmt.Errorf("%w: truncated path length", ErrStoredTree)
		}
		pathLength := uint64(binary.BigEndian.Uint32(raw[offset : offset+4]))
		offset += 4
		if pathLength > uint64(len(raw)-offset) {
			return nil, fmt.Errorf("%w: truncated path", ErrStoredTree)
		}
		pathEnd := offset + int(pathLength)
		filePath := string(raw[offset:pathEnd])
		offset = pathEnd
		if len(raw)-offset < 8 {
			return nil, fmt.Errorf("%w: truncated data length", ErrStoredTree)
		}
		dataLength := binary.BigEndian.Uint64(raw[offset : offset+8])
		offset += 8
		if dataLength > maximumStoredTreeFileBytes || dataLength > uint64(len(raw)-offset) {
			return nil, fmt.Errorf("%w: invalid file data length", ErrStoredTree)
		}
		dataEnd := offset + int(dataLength)
		tree = append(tree, projectstate.File{Path: filePath, Data: bytes.Clone(raw[offset:dataEnd])})
		offset = dataEnd
	}
	if offset != len(raw) {
		return nil, fmt.Errorf("%w: trailing bytes", ErrStoredTree)
	}
	if err := validateStoredTreeBounds(tree, true); err != nil {
		return nil, err
	}
	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return nil, fmt.Errorf("%w: decode: %v", ErrStoredTree, err)
	}
	if err := projectstate.Validate(snapshot); err != nil {
		return nil, fmt.Errorf("%w: validate: %v", ErrStoredTree, err)
	}
	if _, err := projectstate.DigestTree(tree); err != nil {
		return nil, fmt.Errorf("%w: digest: %v", ErrStoredTree, err)
	}
	return tree, nil
}

func validateStoredTreeBounds(tree projectstate.Tree, requireSorted bool) error {
	if len(tree) > maximumStoredTreeFiles {
		return fmt.Errorf("%w: too many files", ErrStoredTree)
	}
	seen := make(map[string]struct{}, len(tree))
	aggregate := 0
	previous := ""
	for index, file := range tree {
		if file.Path == "" || !utf8.ValidString(file.Path) || strings.HasPrefix(file.Path, "/") || strings.Contains(file.Path, `\`) {
			return fmt.Errorf("%w: invalid path", ErrStoredTree)
		}
		if uint64(len(file.Path)) > uint64(^uint32(0)) {
			return fmt.Errorf("%w: path too long", ErrStoredTree)
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return fmt.Errorf("%w: duplicate path", ErrStoredTree)
		}
		seen[file.Path] = struct{}{}
		if requireSorted && index > 0 && file.Path <= previous {
			return fmt.Errorf("%w: paths are not strictly sorted", ErrStoredTree)
		}
		previous = file.Path
		if len(file.Data) > maximumStoredTreeFileBytes {
			return fmt.Errorf("%w: file too large", ErrStoredTree)
		}
		aggregate += len(file.Data)
		if aggregate > maximumStoredTreeDataBytes {
			return fmt.Errorf("%w: aggregate data too large", ErrStoredTree)
		}
	}
	return nil
}

func cloneStoredTree(tree projectstate.Tree) projectstate.Tree {
	cloned := make(projectstate.Tree, len(tree))
	for index, file := range tree {
		cloned[index] = projectstate.File{Path: file.Path, Data: bytes.Clone(file.Data)}
	}
	return cloned
}
