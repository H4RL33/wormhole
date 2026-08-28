package git

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/H4RL33/wormhole/internal/types"
	"github.com/H4RL33/wormhole/internal/types/projectstate"
)

const (
	maximumGitHubTreeEntries = 10_000
	maximumGitHubBlobBytes   = 1 << 20
	maximumGitHubTreeBytes   = 16 << 20
	maximumGitHubJSONBytes   = 32 << 20
)

var (
	githubRepositoryIDPattern = regexp.MustCompile(`^[0-9]+$`)
	githubObjectIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	githubRefPattern          = regexp.MustCompile(`^refs/heads/[A-Za-z0-9._/-]+$`)
)

type GitHubObserver struct {
	apiBase     *url.URL
	client      *http.Client
	credentials GitCredentialSource
	now         func() time.Time
}

type githubRepositoryResponse struct {
	ID json.Number `json:"id"`
}

type githubRefResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	} `json:"object"`
}

type githubCommitResponse struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

type githubTreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size"`
}

type githubTreeResponse struct {
	SHA       string            `json:"sha"`
	Truncated bool              `json:"truncated"`
	Tree      []githubTreeEntry `json:"tree"`
}

type githubBlobResponse struct {
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}

// NewGitHubObserver constructs a dedicated no-redirect GitHub API client.
func NewGitHubObserver(apiBaseURL string, credentials GitCredentialSource) (*GitHubObserver, error) {
	base, err := parseGitHubAPIBase(apiBaseURL)
	if err != nil {
		return nil, err
	}
	return &GitHubObserver{
		apiBase: base, credentials: credentials, now: time.Now,
		client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// ObserveRef resolves the ref once, pins every later request to that commit,
// and imports only canonical .wormhole/ bytes.
func (o *GitHubObserver) ObserveRef(ctx context.Context, repository types.RepositoryIdentity, refName, observerCredentialRef string) (RefObservation, projectstate.Tree, error) {
	if o == nil || o.apiBase == nil || o.client == nil || o.now == nil {
		return RefObservation{}, nil, githubObservationError("observer unavailable", nil)
	}
	if err := validateGitHubObservationInput(repository, refName); err != nil {
		return RefObservation{}, nil, err
	}
	token, err := o.resolveCredential(ctx, observerCredentialRef)
	if err != nil {
		return RefObservation{}, nil, err
	}

	var repositoryResponse githubRepositoryResponse
	if err := o.get(ctx, "/repositories/"+repository.ImmutableID, "", token, &repositoryResponse); err != nil {
		return RefObservation{}, nil, err
	}
	if repositoryResponse.ID.String() != repository.ImmutableID {
		return RefObservation{}, nil, githubObservationError("repository identity mismatch", nil)
	}

	branchName := strings.TrimPrefix(refName, "refs/heads/")
	var refResponse githubRefResponse
	if err := o.get(ctx, "/repositories/"+repository.ImmutableID+"/git/ref/heads/"+url.PathEscape(branchName), "", token, &refResponse); err != nil {
		return RefObservation{}, nil, err
	}
	if refResponse.Ref != refName || refResponse.Object.Type != "commit" || !githubObjectIDPattern.MatchString(refResponse.Object.SHA) {
		return RefObservation{}, nil, githubObservationError("ref response mismatch", nil)
	}
	resolvedCommit := refResponse.Object.SHA

	var commitResponse githubCommitResponse
	if err := o.get(ctx, "/repositories/"+repository.ImmutableID+"/git/commits/"+resolvedCommit, "", token, &commitResponse); err != nil {
		return RefObservation{}, nil, err
	}
	if commitResponse.SHA != resolvedCommit || !githubObjectIDPattern.MatchString(commitResponse.Tree.SHA) {
		return RefObservation{}, nil, githubObservationError("commit response mismatch", nil)
	}

	var treeResponse githubTreeResponse
	if err := o.get(ctx, "/repositories/"+repository.ImmutableID+"/git/trees/"+commitResponse.Tree.SHA, "recursive=1", token, &treeResponse); err != nil {
		return RefObservation{}, nil, err
	}
	if treeResponse.SHA != commitResponse.Tree.SHA || treeResponse.Truncated || len(treeResponse.Tree) > maximumGitHubTreeEntries {
		return RefObservation{}, nil, githubObservationError("tree response mismatch or bound exceeded", nil)
	}

	entries, err := wormholeBlobEntries(treeResponse.Tree)
	if err != nil {
		return RefObservation{}, nil, err
	}
	tree := make(projectstate.Tree, 0, len(entries))
	var aggregateBytes int64
	for _, entry := range entries {
		var blob githubBlobResponse
		if err := o.get(ctx, "/repositories/"+repository.ImmutableID+"/git/blobs/"+entry.SHA, "", token, &blob); err != nil {
			return RefObservation{}, nil, err
		}
		data, err := decodeGitHubBlob(entry, blob)
		if err != nil {
			return RefObservation{}, nil, err
		}
		aggregateBytes += int64(len(data))
		if aggregateBytes > maximumGitHubTreeBytes {
			return RefObservation{}, nil, githubObservationError("aggregate blob bound exceeded", nil)
		}
		tree = append(tree, projectstate.File{Path: strings.TrimPrefix(entry.Path, ".wormhole/"), Data: data})
	}

	snapshot, err := projectstate.DecodeTree(tree)
	if err != nil {
		return RefObservation{}, nil, githubObservationError("decode canonical tree", err)
	}
	if err := projectstate.Validate(snapshot); err != nil {
		return RefObservation{}, nil, githubObservationError("validate canonical tree", err)
	}
	digest, err := projectstate.DigestTree(tree)
	if err != nil {
		return RefObservation{}, nil, githubObservationError("digest canonical tree", err)
	}
	if snapshot.Digest != digest {
		return RefObservation{}, nil, githubObservationError("canonical tree digest mismatch", nil)
	}
	return RefObservation{
		Repository: repository, RefName: refName, CommitSHA: resolvedCommit, ObservedAt: o.now().UTC(),
	}, cloneObservedTree(tree), nil
}

func parseGitHubAPIBase(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || path.Clean(parsed.Path) != normalizedBasePath(parsed.Path) {
		return nil, githubObservationError("invalid API base URL", nil)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed, nil
}

func normalizedBasePath(value string) string {
	if value == "" {
		return "."
	}
	if value == "/" {
		return "/"
	}
	return strings.TrimSuffix(value, "/")
}

func validateGitHubObservationInput(repository types.RepositoryIdentity, refName string) error {
	if err := repository.Validate(); err != nil || repository.Provider != "github" ||
		!githubRepositoryIDPattern.MatchString(repository.ImmutableID) || !canonicalGitHubRepositoryID(repository.ImmutableID) {
		return githubObservationError("invalid repository identity", nil)
	}
	if !githubRefPattern.MatchString(refName) || !validGitHubRef(refName) {
		return githubObservationError("invalid canonical ref", nil)
	}
	return nil
}

func canonicalGitHubRepositoryID(value string) bool {
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && strconv.FormatUint(parsed, 10) == value
}

func validGitHubRef(refName string) bool {
	name := strings.TrimPrefix(refName, "refs/heads/")
	if name == "" || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasPrefix(name, ".") ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") || strings.Contains(name, "..") || strings.Contains(name, "//") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "." || component == ".." {
			return false
		}
	}
	return true
}

func (o *GitHubObserver) resolveCredential(ctx context.Context, reference string) (string, error) {
	if reference == "" {
		return "", nil
	}
	if o.credentials == nil || strings.TrimSpace(reference) != reference {
		return "", githubObservationError("server credential unavailable", nil)
	}
	credential, err := o.credentials.ReadServerCredential(ctx, reference)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", githubObservationError("server credential unavailable", ctxErr)
		}
		return "", githubObservationError("server credential unavailable", nil)
	}
	if credential == "" || strings.TrimSpace(credential) != credential {
		return "", githubObservationError("server credential unavailable", nil)
	}
	for _, character := range credential {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return "", githubObservationError("server credential unavailable", nil)
		}
	}
	return credential, nil
}

func (o *GitHubObserver) get(ctx context.Context, endpointPath, rawQuery, token string, target any) error {
	requestURL := *o.apiBase
	rawPath := strings.TrimSuffix(o.apiBase.EscapedPath(), "/") + endpointPath
	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return githubObservationError("construct request path", err)
	}
	requestURL.Path = decodedPath
	requestURL.RawPath = rawPath
	requestURL.RawQuery = rawQuery
	if requestURL.Scheme != o.apiBase.Scheme || requestURL.Host != o.apiBase.Host || requestURL.User != nil {
		return githubObservationError("request origin mismatch", nil)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return githubObservationError("construct request", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "wormhole-fabric-github-observer-v1")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := o.client.Do(request)
	if err != nil {
		return githubObservationError("perform request", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		return githubObservationError("redirect rejected", nil)
	}
	if response.StatusCode != http.StatusOK {
		return githubObservationError(fmt.Sprintf("provider status %d", response.StatusCode), nil)
	}
	limited := io.LimitReader(response.Body, maximumGitHubJSONBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return githubObservationError("read response", err)
	}
	if len(raw) > maximumGitHubJSONBytes {
		return githubObservationError("response bound exceeded", nil)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return githubObservationError("decode response", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return githubObservationError("trailing response data", nil)
	}
	return nil
}

func wormholeBlobEntries(entries []githubTreeEntry) ([]githubTreeEntry, error) {
	selected := make([]githubTreeEntry, 0)
	for _, entry := range entries {
		if entry.Path == ".wormhole" {
			if entry.Type != "tree" || entry.Mode != "040000" {
				return nil, githubObservationError("invalid .wormhole root entry", nil)
			}
			continue
		}
		if !strings.HasPrefix(entry.Path, ".wormhole/") {
			continue
		}
		if !validWormholeEntryPath(entry.Path) {
			return nil, githubObservationError("path outside .wormhole", nil)
		}
		if entry.Type == "tree" && entry.Mode == "040000" {
			continue
		}
		if entry.Type != "blob" || (entry.Mode != "100644" && entry.Mode != "100755") || !githubObjectIDPattern.MatchString(entry.SHA) {
			return nil, githubObservationError("symlink, submodule, or invalid tree entry", nil)
		}
		if entry.Size != nil && (*entry.Size < 0 || *entry.Size > maximumGitHubBlobBytes) {
			return nil, githubObservationError("blob bound exceeded", nil)
		}
		selected = append(selected, entry)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].Path < selected[j].Path })
	return selected, nil
}

func validWormholeEntryPath(value string) bool {
	if strings.ContainsRune(value, 0) || path.Clean(value) != value || strings.HasPrefix(value, "/") {
		return false
	}
	relative := strings.TrimPrefix(value, ".wormhole/")
	return relative != "" && relative != "." && relative != ".." && !strings.HasPrefix(relative, "../")
}

func decodeGitHubBlob(entry githubTreeEntry, blob githubBlobResponse) ([]byte, error) {
	if blob.SHA != entry.SHA || blob.Encoding != "base64" || blob.Size < 0 || blob.Size > maximumGitHubBlobBytes {
		return nil, githubObservationError("blob response mismatch or bound exceeded", nil)
	}
	if entry.Size != nil && *entry.Size != blob.Size {
		return nil, githubObservationError("tree and blob size mismatch", nil)
	}
	data, err := base64.StdEncoding.DecodeString(blob.Content)
	if err != nil || len(data) > maximumGitHubBlobBytes || int64(len(data)) != blob.Size {
		return nil, githubObservationError("invalid blob content", err)
	}
	return data, nil
}

func githubObservationError(operation string, cause error) error {
	if cause == nil {
		return fmt.Errorf("git: observe GitHub ref: %s: %w", operation, ErrGitObservation)
	}
	return fmt.Errorf("git: observe GitHub ref: %s: %w: %w", operation, ErrGitObservation, cause)
}
