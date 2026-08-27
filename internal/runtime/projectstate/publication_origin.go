package projectstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

var ErrGitOriginChanged = errors.New("projectstate: git origin changed")

const (
	maxPublicationOriginOutputBytes = 16 << 10
	maxPublicationOriginURLs        = 8
	maxPublicationOriginURLBytes    = 4 << 10
	originDigestDomain              = "dev.wormhole.workspace-origin.v1\x00"
	publicationBindingDigestDomain  = "dev.wormhole.setup-publication-binding.v1\x00"
)

type observedOriginV1 struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Host          string `json:"host,omitempty"`
	Port          string `json:"port,omitempty"`
	Path          string `json:"path,omitempty"`
	AbsolutePath  string `json:"absolute_path,omitempty"`
}

type setupPublicationBindingV1 struct {
	SchemaVersion int                      `json:"schema_version"`
	Kind          string                   `json:"kind"`
	Repository    types.RepositoryIdentity `json:"repository_identity"`
	OriginDigest  state.Digest             `json:"origin_digest"`
}

type publicationOriginObservation struct {
	root     string
	checkout types.CheckoutIdentity
	origin   observedOriginV1
	digest   state.Digest
}

type publicationOriginReaders struct {
	canonicalRoot    func(string) (string, error)
	checkoutIdentity func(string) (types.CheckoutIdentity, error)
	validateGitRoot  func(context.Context, string) error
	readOrigin       func(context.Context, string) ([]byte, int, bool, error)
}

func InspectPublicationOrigin(ctx context.Context, requestedRoot string) (state.Digest, error) {
	observed, err := observePublicationOrigin(ctx, requestedRoot)
	if err != nil {
		return "", err
	}
	return observed.digest, nil
}

func observePublicationOrigin(ctx context.Context, requestedRoot string) (publicationOriginObservation, error) {
	return observePublicationOriginWithReaders(ctx, requestedRoot, publicationOriginReaders{
		canonicalRoot: canonicalNonSymlinkDirectory, checkoutIdentity: checkoutIdentity,
		validateGitRoot: validatePublicationGitRoot, readOrigin: readPublicationOriginGit,
	})
}

func observePublicationOriginWithReaders(
	ctx context.Context,
	requestedRoot string,
	readers publicationOriginReaders,
) (publicationOriginObservation, error) {
	if readers.canonicalRoot == nil || readers.checkoutIdentity == nil || readers.validateGitRoot == nil || readers.readOrigin == nil {
		return publicationOriginObservation{}, fmt.Errorf("projectstate: publication origin observer is unavailable")
	}
	root, err := readers.canonicalRoot(requestedRoot)
	if err != nil || root != requestedRoot {
		return publicationOriginObservation{}, fmt.Errorf("projectstate: publication origin requires the exact canonical checkout root")
	}
	checkout, err := readers.checkoutIdentity(root)
	if err != nil {
		return publicationOriginObservation{}, fmt.Errorf("projectstate: inspect publication origin checkout: %w", err)
	}
	if err := readers.validateGitRoot(ctx, root); err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return publicationOriginObservation{}, contextError
		}
		return publicationOriginObservation{}, fmt.Errorf("projectstate: publication origin requires the exact Git checkout root")
	}
	output, status, missing, err := readers.readOrigin(ctx, root)
	if err != nil {
		if contextError := ctx.Err(); contextError != nil {
			return publicationOriginObservation{}, contextError
		}
		return publicationOriginObservation{}, fmt.Errorf("projectstate: publication origin observation failed")
	}
	origin, err := parsePublicationOriginOutput(root, output, status, missing)
	if err != nil {
		return publicationOriginObservation{}, err
	}
	finalRoot, err := readers.canonicalRoot(requestedRoot)
	if err != nil || finalRoot != root {
		return publicationOriginObservation{}, fmt.Errorf("%w: checkout root changed", ErrGitOriginChanged)
	}
	finalCheckout, err := readers.checkoutIdentity(finalRoot)
	if err != nil {
		return publicationOriginObservation{}, fmt.Errorf("%w: revalidate checkout identity", ErrGitOriginChanged)
	}
	if finalCheckout != checkout {
		return publicationOriginObservation{}, fmt.Errorf("%w: checkout identity changed", ErrGitOriginChanged)
	}
	digest, err := digestObservedOrigin(origin)
	if err != nil {
		return publicationOriginObservation{}, err
	}
	return publicationOriginObservation{root: root, checkout: checkout, origin: origin, digest: digest}, nil
}

func reobservePublicationOrigin(ctx context.Context, outside publicationOriginObservation) error {
	return reobservePublicationOriginWithReader(ctx, outside, observePublicationOrigin)
}

func reobservePublicationOriginWithReader(
	ctx context.Context,
	outside publicationOriginObservation,
	reader func(context.Context, string) (publicationOriginObservation, error),
) error {
	if reader == nil {
		return fmt.Errorf("%w: publication origin observer is unavailable", ErrGitOriginChanged)
	}
	if err := validatePublicationOriginObservation(outside); err != nil {
		return fmt.Errorf("%w: invalid outside publication origin observation", ErrGitOriginChanged)
	}
	inside, err := reader(ctx, outside.root)
	if err != nil {
		return fmt.Errorf("%w: reobserve publication origin", ErrGitOriginChanged)
	}
	if err := validatePublicationOriginObservation(inside); err != nil {
		return fmt.Errorf("%w: invalid inside publication origin observation", ErrGitOriginChanged)
	}
	if inside.root != outside.root || inside.checkout != outside.checkout || inside.origin != outside.origin || inside.digest != outside.digest {
		return fmt.Errorf("%w: root, checkout, or origin differs", ErrGitOriginChanged)
	}
	return nil
}

func validatePublicationOriginObservation(observed publicationOriginObservation) error {
	if !filepath.IsAbs(observed.root) || filepath.Clean(observed.root) != observed.root ||
		observed.checkout.CanonicalPath != observed.root || observed.checkout.Device == 0 || observed.checkout.Inode == 0 {
		return fmt.Errorf("projectstate: invalid publication origin checkout observation")
	}
	digest, err := digestObservedOrigin(observed.origin)
	if err != nil || digest != observed.digest {
		return fmt.Errorf("projectstate: invalid publication origin digest observation")
	}
	return nil
}

func readPublicationOriginGit(ctx context.Context, root string) ([]byte, int, bool, error) {
	return readPublicationGitBounded(ctx, root, maxPublicationOriginOutputBytes, "remote", "get-url", "--all", "origin")
}

func validatePublicationGitRoot(ctx context.Context, root string) error {
	output, status, _, err := readPublicationGitBounded(ctx, root, maxPublicationOriginOutputBytes,
		"rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return err
	}
	if status != 0 || len(output) < 2 || output[len(output)-1] != '\n' || bytes.IndexByte(output[:len(output)-1], '\n') >= 0 {
		return fmt.Errorf("projectstate: invalid Git checkout root observation")
	}
	observed := string(output[:len(output)-1])
	if invalidOriginText(observed) || !filepath.IsAbs(observed) || filepath.Clean(observed) != observed || observed != root {
		return fmt.Errorf("projectstate: requested root is not the exact Git checkout root")
	}
	return nil
}

func readPublicationGitBounded(ctx context.Context, root string, limit int, arguments ...string) ([]byte, int, bool, error) {
	if limit < 0 {
		return nil, 0, false, fmt.Errorf("projectstate: invalid publication Git output limit")
	}
	command := newReadOnlyGitCommand(ctx, root, arguments...)
	command.Stdin = bytes.NewReader(nil)
	stderr := newBoundedGitStderr()
	command.Stderr = stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, 0, false, fmt.Errorf("projectstate: open publication origin output")
	}
	if err := command.Start(); err != nil {
		return nil, 0, false, fmt.Errorf("projectstate: start publication origin observation")
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	if len(output) > limit || readErr != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if err := ctx.Err(); err != nil {
		return nil, 0, false, err
	}
	if len(output) > limit {
		return nil, 0, false, fmt.Errorf("projectstate: publication origin output exceeds size limit")
	}
	if readErr != nil {
		return nil, 0, false, fmt.Errorf("projectstate: read publication origin output")
	}
	if stderr.truncated {
		return nil, 0, false, fmt.Errorf("projectstate: publication origin diagnostic exceeds size limit")
	}
	if waitErr == nil {
		return bytes.Clone(output), 0, false, nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		status := exitError.ExitCode()
		missing := status == 2 && bytes.Equal(stderr.buffer.Bytes(), []byte("error: No such remote 'origin'\n"))
		return bytes.Clone(output), status, missing, nil
	}
	return nil, 0, false, fmt.Errorf("projectstate: publication origin Git command failed")
}

func parsePublicationOriginOutput(root string, output []byte, status int, missing bool) (observedOriginV1, error) {
	if len(output) > maxPublicationOriginOutputBytes {
		return observedOriginV1{}, fmt.Errorf("projectstate: publication origin output exceeds size limit")
	}
	if status == 2 && missing && len(output) == 0 {
		return observedOriginV1{SchemaVersion: 1, Kind: "missing"}, nil
	}
	if status != 0 || missing {
		return observedOriginV1{}, fmt.Errorf("projectstate: publication origin Git command failed with status %d", status)
	}
	if len(output) == 0 {
		return observedOriginV1{SchemaVersion: 1, Kind: "missing"}, nil
	}
	if output[len(output)-1] != '\n' {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	entries := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(entries) == 0 || len(entries) > maxPublicationOriginURLs {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	var canonical observedOriginV1
	for index, entry := range entries {
		if len(entry) == 0 || len(entry) > maxPublicationOriginURLBytes {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
		origin, err := normalizePublicationOrigin(root, string(entry))
		if err != nil {
			return observedOriginV1{}, err
		}
		if index == 0 {
			canonical = origin
		} else if origin != canonical {
			return observedOriginV1{}, fmt.Errorf("projectstate: ambiguous publication origin")
		}
	}
	return canonical, nil
}

func normalizePublicationOrigin(root, raw string) (observedOriginV1, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || invalidOriginText(raw) || len(raw) > maxPublicationOriginURLBytes {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	if strings.Contains(raw, "\\") || strings.Contains(raw, "%") {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "file:") {
		return normalizeFilesystemOrigin(root, raw)
	}
	if strings.Contains(raw, "://") {
		return normalizeNetworkOrigin(raw)
	}
	if delimiter := strings.IndexByte(raw, ':'); delimiter > 0 {
		switch strings.ToLower(raw[:delimiter]) {
		case "http", "https", "ssh", "git":
			return observedOriginV1{}, invalidPublicationOrigin()
		}
	}
	if authority, repositoryPath, ok := splitSCPLikeOrigin(raw); ok {
		return normalizeSCPOrigin(authority, repositoryPath)
	}
	return normalizeFilesystemPath(root, raw)
}

func normalizeNetworkOrigin(raw string) (observedOriginV1, error) {
	parsed, err := url.Parse(raw)
	if err != nil || strings.Contains(raw, "#") || parsed.Opaque != "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	scheme := strings.ToLower(parsed.Scheme)
	if parsed.Scheme != scheme {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	defaults := map[string]string{"http": "80", "https": "443", "ssh": "22", "git": "9418"}
	defaultPort, supported := defaults[scheme]
	if !supported {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	if parsed.User != nil {
		if scheme != "ssh" || parsed.User.Username() != "git" {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
	}
	if err := validateNetworkAuthority(parsed); err != nil {
		return observedOriginV1{}, err
	}
	host, err := canonicalOriginHost(parsed.Hostname())
	if err != nil {
		return observedOriginV1{}, err
	}
	if strings.Contains(parsed.Hostname(), ":") {
		hostPort := parsed.Host
		if parsed.User != nil {
			hostPort = strings.TrimPrefix(hostPort, parsed.User.String()+"@")
		}
		if !strings.HasPrefix(hostPort, "[") {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
	}
	port, err := canonicalOriginPort(parsed.Port(), defaultPort)
	if err != nil {
		return observedOriginV1{}, err
	}
	repositoryPath, err := canonicalOriginRepositoryPath(parsed.Path)
	if err != nil {
		return observedOriginV1{}, err
	}
	return observedOriginV1{SchemaVersion: 1, Kind: "network", Host: host, Port: port, Path: repositoryPath}, nil
}

func validateNetworkAuthority(parsed *url.URL) error {
	hostPort := parsed.Host
	if delimiter := strings.LastIndexByte(hostPort, '@'); delimiter >= 0 {
		hostPort = hostPort[delimiter+1:]
	}
	if strings.HasPrefix(hostPort, "[") {
		closing := strings.LastIndexByte(hostPort, ']')
		if closing < 0 {
			return invalidPublicationOrigin()
		}
		suffix := hostPort[closing+1:]
		if suffix != "" && (!strings.HasPrefix(suffix, ":") || len(suffix) == 1) {
			return invalidPublicationOrigin()
		}
		return nil
	}
	if strings.Count(hostPort, ":") > 1 || strings.HasSuffix(hostPort, ":") {
		return invalidPublicationOrigin()
	}
	return nil
}

func splitSCPLikeOrigin(raw string) (string, string, bool) {
	if strings.HasPrefix(raw, "/") {
		return "", "", false
	}
	if closing := strings.Index(raw, "]:"); closing >= 0 {
		if strings.Contains(raw[:closing], "/") {
			return "", "", false
		}
		return raw[:closing+1], raw[closing+2:], true
	}
	delimiter := strings.IndexByte(raw, ':')
	if delimiter <= 0 || strings.Contains(raw[:delimiter], "/") {
		return "", "", false
	}
	return raw[:delimiter], raw[delimiter+1:], true
}

func normalizeSCPOrigin(authority, rawPath string) (observedOriginV1, error) {
	username := ""
	hostText := authority
	if strings.Count(authority, "@") > 1 {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	if before, after, found := strings.Cut(authority, "@"); found {
		if before == "" {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
		username, hostText = before, after
	}
	if username != "" && username != "git" {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	if strings.HasPrefix(hostText, "[") {
		if !strings.HasSuffix(hostText, "]") {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
		hostText = strings.TrimSuffix(strings.TrimPrefix(hostText, "["), "]")
	} else if strings.Contains(hostText, ":") {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	host, err := canonicalOriginHost(hostText)
	if err != nil {
		return observedOriginV1{}, err
	}
	repositoryPath, err := canonicalOriginRepositoryPath(rawPath)
	if err != nil {
		return observedOriginV1{}, err
	}
	return observedOriginV1{SchemaVersion: 1, Kind: "network", Host: host, Path: repositoryPath}, nil
}

func canonicalOriginHost(raw string) (string, error) {
	if raw == "" || !asciiOnly(raw) {
		return "", invalidPublicationOrigin()
	}
	if strings.Contains(raw, ":") {
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() != nil {
			return "", invalidPublicationOrigin()
		}
		return ip.String(), nil
	}
	if strings.Contains(raw, ".") && allDigitsAndDots(raw) {
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() == nil || ip.String() != raw {
			return "", invalidPublicationOrigin()
		}
		return raw, nil
	}
	host := strings.ToLower(raw)
	host = strings.TrimSuffix(host, ".")
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return "", invalidPublicationOrigin()
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", invalidPublicationOrigin()
		}
		for _, char := range []byte(label) {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", invalidPublicationOrigin()
			}
		}
	}
	return host, nil
}

func canonicalOriginPort(raw, defaultPort string) (string, error) {
	if raw == "" {
		return "", nil
	}
	for _, char := range []byte(raw) {
		if char < '0' || char > '9' {
			return "", invalidPublicationOrigin()
		}
	}
	value, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || value == 0 {
		return "", invalidPublicationOrigin()
	}
	canonical := strconv.FormatUint(value, 10)
	if canonical == defaultPort {
		return "", nil
	}
	return canonical, nil
}

func canonicalOriginRepositoryPath(raw string) (string, error) {
	trimmed := strings.TrimRight(raw, "/")
	trimmed = strings.TrimPrefix(trimmed, "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	if trimmed == "" || strings.HasPrefix(trimmed, "/") || !asciiOnly(trimmed) || path.Clean(trimmed) != trimmed {
		return "", invalidPublicationOrigin()
	}
	for _, char := range []byte(trimmed) {
		if char <= 0x20 || char == 0x7f || char == '%' || char == '\\' || char == '?' || char == '#' {
			return "", invalidPublicationOrigin()
		}
	}
	for _, component := range strings.Split(trimmed, "/") {
		if component == "" || component == "." || component == ".." {
			return "", invalidPublicationOrigin()
		}
	}
	return trimmed, nil
}

func normalizeFilesystemOrigin(root, raw string) (observedOriginV1, error) {
	parsed, err := url.Parse(raw)
	if err != nil || strings.Contains(raw, "#") || strings.ToLower(parsed.Scheme) != "file" || parsed.Scheme != strings.ToLower(parsed.Scheme) || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	if parsed.Port() != "" {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	value := parsed.Path
	if parsed.Opaque != "" {
		if parsed.Host != "" || value != "" {
			return observedOriginV1{}, invalidPublicationOrigin()
		}
		value = parsed.Opaque
	}
	return normalizeFilesystemPath(root, value)
}

func normalizeFilesystemPath(root, raw string) (observedOriginV1, error) {
	if raw == "" || invalidOriginText(raw) || strings.ContainsAny(raw, "\\%") || filepath.Clean(raw) != raw {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	absolute := raw
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, absolute)
	}
	absolute = filepath.Clean(absolute)
	if !filepath.IsAbs(absolute) || invalidOriginText(absolute) {
		return observedOriginV1{}, invalidPublicationOrigin()
	}
	return observedOriginV1{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: absolute}, nil
}

func digestObservedOrigin(origin observedOriginV1) (state.Digest, error) {
	if err := validateObservedOrigin(origin); err != nil {
		return "", err
	}
	return digestDomainCanonicalJSON(originDigestDomain, origin)
}

func validateObservedOrigin(origin observedOriginV1) error {
	if origin.SchemaVersion != 1 {
		return invalidPublicationOrigin()
	}
	switch origin.Kind {
	case "missing":
		if origin.Host != "" || origin.Port != "" || origin.Path != "" || origin.AbsolutePath != "" {
			return invalidPublicationOrigin()
		}
	case "network":
		if origin.AbsolutePath != "" {
			return invalidPublicationOrigin()
		}
		host, err := canonicalOriginHost(origin.Host)
		if err != nil || host != origin.Host {
			return invalidPublicationOrigin()
		}
		port, err := canonicalOriginPort(origin.Port, "")
		if err != nil || port != origin.Port {
			return invalidPublicationOrigin()
		}
		repositoryPath, err := canonicalOriginRepositoryPath(origin.Path)
		if err != nil || repositoryPath != origin.Path {
			return invalidPublicationOrigin()
		}
	case "filesystem":
		if origin.Host != "" || origin.Port != "" || origin.Path != "" || origin.AbsolutePath == "" ||
			!filepath.IsAbs(origin.AbsolutePath) || filepath.Clean(origin.AbsolutePath) != origin.AbsolutePath ||
			invalidOriginText(origin.AbsolutePath) || strings.ContainsAny(origin.AbsolutePath, "\\%") {
			return invalidPublicationOrigin()
		}
	default:
		return invalidPublicationOrigin()
	}
	return nil
}

func DigestPublicationBindingConstraint(repository types.RepositoryIdentity, origin state.Digest) (state.Digest, error) {
	if err := repository.Validate(); err != nil || !validPublicationDigest(origin) {
		return "", fmt.Errorf("projectstate: invalid publication binding constraint")
	}
	return digestDomainCanonicalJSON(publicationBindingDigestDomain, setupPublicationBindingV1{
		SchemaVersion: 1, Kind: "setup_publication_binding", Repository: repository, OriginDigest: origin,
	})
}

func digestDomainCanonicalJSON(domain string, value any) (state.Digest, error) {
	canonical, err := state.CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write(canonical)
	return state.Digest("sha256:" + hex.EncodeToString(hash.Sum(nil))), nil
}

func validPublicationDigest(digest state.Digest) bool {
	value := string(digest)
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, char := range []byte(strings.TrimPrefix(value, "sha256:")) {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func invalidOriginText(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return true
	}
	for _, char := range []byte(value) {
		if char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func asciiOnly(value string) bool {
	for _, char := range []byte(value) {
		if char >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func allDigitsAndDots(value string) bool {
	for _, char := range []byte(value) {
		if (char < '0' || char > '9') && char != '.' {
			return false
		}
	}
	return true
}

func invalidPublicationOrigin() error {
	return fmt.Errorf("projectstate: invalid publication origin")
}
