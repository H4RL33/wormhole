// Package source assembles exact, hash-validated source slices under explicit
// authorization and byte ceilings.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxCandidates       = 1_000
	MaxIndexedFileBytes = 4 << 20
	maxFollowUps        = 32
)

var ErrInvalidRequest = errors.New("codegraph source: invalid request")
var ErrContainment = errors.New("codegraph source: containment rejected")

type Completeness string

const (
	CompletenessComplete     Completeness = "complete"
	CompletenessPartial      Completeness = "partial"
	CompletenessMetadataOnly Completeness = "metadata_only"
)

const (
	OmissionMissingPermission  = "missing_permission"
	OmissionBudgetZero         = "source_budget_zero"
	OmissionBudgetExhausted    = "source_budget_exhausted"
	OmissionWorkingTreeChanged = "working_tree_changed"
)

type Candidate struct {
	SymbolID        string
	QualifiedName   string
	FilePath        string
	IndexedHash     string
	IndexedByteSize int64
	StartByte       int64
	EndByte         int64
	StartLine       int
	EndLine         int
	Rank            int
}

type Request struct {
	Checkout       string
	Authorized     bool
	RequestedBytes int64
	ProjectCeiling int64
	GlobalCeiling  int64
	Candidates     []Candidate
}

type Outcome struct {
	SymbolID           string
	QualifiedName      string
	FilePath           string
	StartByte          int64
	EndByte            int64
	StartLine          int
	EndLine            int
	SourceIncluded     bool
	Source             string
	ReturnedBytes      int64
	OmissionReason     string
	RefreshRecommended bool
	RequiredPermission string
}

type Result struct {
	Outcomes                 []Outcome
	EffectiveBudget          int64
	ReturnedBytes            int64
	Completeness             Completeness
	OmittedNodeCount         int
	OmissionReason           string
	SuggestedFollowUpSymbols []string
}

// Assemble validates authorization before any filesystem operation, then uses
// one root-scoped handle and one capped read per indexed file.
func Assemble(ctx context.Context, request Request) (Result, error) {
	return assemble(ctx, request, nil, nil)
}

type afterRootOpenHook func()
type beforeFileOpenHook func(string)

func assemble(ctx context.Context, request Request, afterRootOpen afterRootOpenHook, beforeFileOpen beforeFileOpenHook) (Result, error) {
	candidates := append([]Candidate(nil), request.Candidates...)
	sortCandidates(candidates)
	if !request.Authorized {
		return metadataOnly(candidates, OmissionMissingPermission, "code_graph.source.read"), nil
	}
	if request.RequestedBytes == 0 {
		return metadataOnly(candidates, OmissionBudgetZero, ""), nil
	}
	if err := validateRequest(request, candidates); err != nil {
		return Result{}, err
	}
	effective := minimum(request.RequestedBytes, request.ProjectCeiling, request.GlobalCeiling)
	result := Result{EffectiveBudget: effective, Completeness: CompletenessComplete, Outcomes: make([]Outcome, 0, len(candidates))}
	if len(candidates) == 0 {
		return result, nil
	}
	root, err := os.OpenRoot(request.Checkout)
	if err != nil {
		return Result{}, fmt.Errorf("%w: open approved checkout", ErrContainment)
	}
	defer root.Close()
	if afterRootOpen != nil {
		afterRootOpen()
	}
	cache := make(map[string]fileResult)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		outcome := baseOutcome(candidate)
		sliceBytes := candidate.EndByte - candidate.StartByte
		if sliceBytes > effective-result.ReturnedBytes {
			outcome.OmissionReason = OmissionBudgetExhausted
			appendOmitted(&result, outcome)
			continue
		}
		key := strings.Join([]string{candidate.FilePath, candidate.IndexedHash, fmt.Sprint(candidate.IndexedByteSize)}, "\x00")
		file, exists := cache[key]
		if !exists {
			if beforeFileOpen != nil {
				beforeFileOpen(candidate.FilePath)
			}
			file, err = readIndexedFile(root, candidate)
			if err != nil {
				return Result{}, err
			}
			cache[key] = file
		}
		if !file.current {
			outcome.OmissionReason = OmissionWorkingTreeChanged
			outcome.RefreshRecommended = true
			appendOmitted(&result, outcome)
			continue
		}
		if candidate.EndByte > int64(len(file.content)) || !lineRangeMatches(file.content, candidate) {
			return Result{}, fmt.Errorf("%w: indexed source range is inconsistent", ErrInvalidRequest)
		}
		slice := file.content[candidate.StartByte:candidate.EndByte]
		if !utf8.Valid(slice) {
			return Result{}, fmt.Errorf("%w: indexed source range splits invalid UTF-8", ErrInvalidRequest)
		}
		outcome.SourceIncluded = true
		outcome.Source = string(slice)
		outcome.ReturnedBytes = int64(len(slice))
		result.ReturnedBytes += outcome.ReturnedBytes
		result.Outcomes = append(result.Outcomes, outcome)
	}
	if result.OmittedNodeCount > 0 {
		result.Completeness = CompletenessPartial
	}
	return result, nil
}

type fileResult struct {
	content []byte
	current bool
}

func readIndexedFile(root *os.Root, candidate Candidate) (fileResult, error) {
	file, err := root.Open(filepath.FromSlash(candidate.FilePath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fileResult{current: false}, nil
		}
		return fileResult{}, fmt.Errorf("%w: root-scoped open %q", ErrContainment, candidate.FilePath)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileResult{}, fmt.Errorf("%w: stat indexed file", ErrContainment)
	}
	if !info.Mode().IsRegular() {
		return fileResult{}, fmt.Errorf("%w: indexed path is not regular", ErrContainment)
	}
	if info.Size() != candidate.IndexedByteSize {
		return fileResult{current: false}, nil
	}
	content, err := io.ReadAll(io.LimitReader(file, candidate.IndexedByteSize+1))
	if err != nil {
		return fileResult{}, fmt.Errorf("codegraph source: read indexed file: %w", err)
	}
	if int64(len(content)) != candidate.IndexedByteSize {
		return fileResult{current: false}, nil
	}
	digest := sha256.Sum256(content)
	if "sha256:"+hex.EncodeToString(digest[:]) != candidate.IndexedHash {
		return fileResult{current: false}, nil
	}
	return fileResult{content: content, current: true}, nil
}

func validateRequest(request Request, candidates []Candidate) error {
	if request.RequestedBytes < 0 || request.ProjectCeiling <= 0 || request.GlobalCeiling <= 0 || len(candidates) > MaxCandidates {
		return fmt.Errorf("%w: invalid source budget", ErrInvalidRequest)
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.SymbolID == "" {
			return fmt.Errorf("%w: candidate symbol id is required", ErrInvalidRequest)
		}
		if _, duplicate := seen[candidate.SymbolID]; duplicate {
			return fmt.Errorf("%w: duplicate candidate", ErrInvalidRequest)
		}
		seen[candidate.SymbolID] = struct{}{}
		if err := validatePath(candidate.FilePath); err != nil {
			return err
		}
		if !validSHA256(candidate.IndexedHash) || candidate.IndexedByteSize < 0 || candidate.IndexedByteSize > MaxIndexedFileBytes {
			return fmt.Errorf("%w: malformed indexed file metadata", ErrInvalidRequest)
		}
		if candidate.StartByte < 0 || candidate.EndByte <= candidate.StartByte || candidate.EndByte > candidate.IndexedByteSize ||
			candidate.StartLine < 1 || candidate.EndLine < candidate.StartLine {
			return fmt.Errorf("%w: malformed indexed source range", ErrInvalidRequest)
		}
	}
	return nil
}

func validatePath(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.Contains(value, "\\") || strings.ContainsRune(value, 0) ||
		path.Clean(value) != value || !filepath.IsLocal(filepath.FromSlash(value)) {
		return fmt.Errorf("%w: unsafe indexed path", ErrContainment)
	}
	return nil
}

func lineRangeMatches(content []byte, candidate Candidate) bool {
	if candidate.StartByte < 0 || candidate.EndByte > int64(len(content)) || candidate.EndByte <= candidate.StartByte {
		return false
	}
	startLine := 1 + countNewlines(content[:candidate.StartByte])
	endLine := 1 + countNewlines(content[:candidate.EndByte-1])
	return startLine == candidate.StartLine && endLine == candidate.EndLine
}

func countNewlines(content []byte) int {
	count := 0
	for _, value := range content {
		if value == '\n' {
			count++
		}
	}
	return count
}

func metadataOnly(candidates []Candidate, reason, permission string) Result {
	result := Result{
		Completeness: CompletenessMetadataOnly, OmittedNodeCount: len(candidates), OmissionReason: reason,
		Outcomes: make([]Outcome, 0, len(candidates)),
	}
	for _, candidate := range candidates {
		outcome := baseOutcome(candidate)
		outcome.OmissionReason = reason
		outcome.RequiredPermission = permission
		result.Outcomes = append(result.Outcomes, outcome)
		if candidate.QualifiedName != "" && len(result.SuggestedFollowUpSymbols) < maxFollowUps {
			result.SuggestedFollowUpSymbols = append(result.SuggestedFollowUpSymbols, candidate.QualifiedName)
		}
	}
	return result
}

func appendOmitted(result *Result, outcome Outcome) {
	result.Outcomes = append(result.Outcomes, outcome)
	result.OmittedNodeCount++
	if result.OmissionReason == "" {
		result.OmissionReason = outcome.OmissionReason
	}
	if outcome.QualifiedName != "" && len(result.SuggestedFollowUpSymbols) < maxFollowUps {
		result.SuggestedFollowUpSymbols = append(result.SuggestedFollowUpSymbols, outcome.QualifiedName)
	}
}

func baseOutcome(candidate Candidate) Outcome {
	return Outcome{
		SymbolID: candidate.SymbolID, QualifiedName: candidate.QualifiedName, FilePath: candidate.FilePath,
		StartByte: candidate.StartByte, EndByte: candidate.EndByte, StartLine: candidate.StartLine, EndLine: candidate.EndLine,
	}
}

func sortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		leftRank, rightRank := candidates[i].Rank, candidates[j].Rank
		if leftRank <= 0 {
			leftRank = int(^uint(0) >> 1)
		}
		if rightRank <= 0 {
			rightRank = int(^uint(0) >> 1)
		}
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		if candidates[i].QualifiedName != candidates[j].QualifiedName {
			return candidates[i].QualifiedName < candidates[j].QualifiedName
		}
		return candidates[i].SymbolID < candidates[j].SymbolID
	})
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	digest := value[len("sha256:"):]
	_, err := hex.DecodeString(digest)
	return err == nil && strings.ToLower(digest) == digest
}

func minimum(values ...int64) int64 {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}
