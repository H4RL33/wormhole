package projectstate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/H4RL33/wormhole/internal/types"
	state "github.com/H4RL33/wormhole/internal/types/projectstate"
)

func TestObservedPublicationOriginDigestGoldens(t *testing.T) {
	tests := []struct {
		name       string
		origin     observedOriginV1
		wantJSON   string
		wantDigest state.Digest
	}{
		{
			name: "missing", origin: observedOriginV1{SchemaVersion: 1, Kind: "missing"},
			wantJSON:   "{\"schema_version\":1,\"kind\":\"missing\"}\n",
			wantDigest: "sha256:aaf73d9f175b4871b3ba05456155e482bbe9e4c11238ed4ad5a53323e05fe083",
		},
		{
			name: "network", origin: observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "github.com", Path: "Acme/Repo"},
			wantJSON:   "{\"schema_version\":1,\"kind\":\"network\",\"host\":\"github.com\",\"path\":\"Acme/Repo\"}\n",
			wantDigest: "sha256:74db0db09b5ca872cf5dc87bb3396d3c4189e9221546e5868a9d11a8966651b9",
		},
		{
			name: "filesystem", origin: observedOriginV1{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/work/upstream"},
			wantJSON:   "{\"schema_version\":1,\"kind\":\"filesystem\",\"absolute_path\":\"/work/upstream\"}\n",
			wantDigest: "sha256:b3a3e406f1900f5517842ed10057c9bfdcde0dd67acabe3b02029f67797e0d41",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := state.CanonicalJSON(test.origin)
			if err != nil || string(canonical) != test.wantJSON {
				t.Fatalf("CanonicalJSON() = (%q, %v), want (%q, nil)", canonical, err, test.wantJSON)
			}
			digest, err := digestObservedOrigin(test.origin)
			if err != nil || digest != test.wantDigest {
				t.Fatalf("digestObservedOrigin() = (%q, %v), want (%q, nil)", digest, err, test.wantDigest)
			}
		})
	}
}

func TestObservedPublicationOriginRejectsNonUnionAndNoncanonicalValues(t *testing.T) {
	for _, origin := range []observedOriginV1{
		{},
		{SchemaVersion: 2, Kind: "missing"},
		{SchemaVersion: 1, Kind: "unknown"},
		{SchemaVersion: 1, Kind: "missing", Host: "example.com"},
		{SchemaVersion: 1, Kind: "network", Host: "EXAMPLE.COM", Path: "acme/repo"},
		{SchemaVersion: 1, Kind: "network", Host: "example.com", Port: "080", Path: "acme/repo"},
		{SchemaVersion: 1, Kind: "network", Host: "example.com", Path: "acme/../repo"},
		{SchemaVersion: 1, Kind: "network", Host: "example.com", Path: "acme/repo", AbsolutePath: "/tmp/repo"},
		{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "relative"},
		{SchemaVersion: 1, Kind: "filesystem", Host: "example.com", AbsolutePath: "/tmp/repo"},
		{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/tmp/with\\backslash"},
		{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/tmp/with%percent"},
	} {
		if digest, err := digestObservedOrigin(origin); err == nil || digest != "" {
			t.Errorf("digestObservedOrigin(%+v) = (%q, %v), want zero and error", origin, digest, err)
		}
	}
}

func TestNormalizePublicationOriginTransportEquivalenceAndCase(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "checkout")
	locators := []string{
		"ssh://git@github.com/Acme/Repo.git",
		"git@github.com:Acme/Repo.git",
		"https://GITHUB.COM./Acme/Repo/",
		"git://github.com:09418/Acme/Repo.git",
	}
	want := observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "github.com", Path: "Acme/Repo"}
	for _, locator := range locators {
		got, err := normalizePublicationOrigin(root, locator)
		if err != nil || got != want {
			t.Errorf("normalizePublicationOrigin(locator) = (%+v, %v), want (%+v, nil)", got, err, want)
		}
	}

	caseChanged, err := normalizePublicationOrigin(root, "https://github.com/acme/repo")
	if err != nil {
		t.Fatal(err)
	}
	if caseChanged.Path != "acme/repo" || caseChanged == want {
		t.Fatalf("path case was not preserved: %+v", caseChanged)
	}
}

func TestNormalizePublicationOriginPortsHostsAndFilesystems(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "checkout")
	tests := []struct {
		name    string
		raw     string
		want    observedOriginV1
		wantErr bool
	}{
		{name: "nondefault port", raw: "ssh://git@example.com:0022/path/repo", want: observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "example.com", Path: "path/repo"}},
		{name: "retained port", raw: "https://example.com:0443/path/repo", want: observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "example.com", Path: "path/repo"}},
		{name: "different port", raw: "https://example.com:8443/path/repo", want: observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "example.com", Port: "8443", Path: "path/repo"}},
		{name: "IPv4", raw: "https://192.0.2.1/path/repo", want: observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "192.0.2.1", Path: "path/repo"}},
		{name: "IPv6", raw: "ssh://git@[2001:0DB8::1]:22/path/repo", want: observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "2001:db8::1", Path: "path/repo"}},
		{name: "absolute path", raw: "/srv/repos/project.git", want: observedOriginV1{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/srv/repos/project.git"}},
		{name: "relative path", raw: "../upstream", want: observedOriginV1{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/work/upstream"}},
		{name: "filesystem hash", raw: "../up#stream", want: observedOriginV1{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/work/up#stream"}},
		{name: "file URL", raw: "file://localhost/srv/repos/project", want: observedOriginV1{SchemaVersion: 1, Kind: "filesystem", AbsolutePath: "/srv/repos/project"}},
		{name: "invalid port zero", raw: "https://example.com:0/path/repo", wantErr: true},
		{name: "invalid port high", raw: "https://example.com:65536/path/repo", wantErr: true},
		{name: "unbracketed IPv6", raw: "ssh://git@2001:db8::1/path/repo", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizePublicationOrigin(root, test.raw)
			if test.wantErr {
				if err == nil || got != (observedOriginV1{}) {
					t.Fatalf("normalizePublicationOrigin() = (%+v, %v), want zero and error", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("normalizePublicationOrigin() = (%+v, %v), want (%+v, nil)", got, err, test.want)
			}
		})
	}
}

func TestDigestPublicationBindingConstraintGoldenAndValidation(t *testing.T) {
	repository := types.RepositoryIdentity{
		Provider: "github", ImmutableID: "repository-1",
		CanonicalRemote: "https://github.com/acme/wormhole",
	}
	origin := state.Digest("sha256:" + strings.Repeat("a", 64))
	const want = state.Digest("sha256:55cfd672d9c515139e3ed07ac6917e2615334bcd84699e57dcdcb55efeefe03e")
	const wantJSON = "{\"schema_version\":1,\"kind\":\"setup_publication_binding\",\"repository_identity\":{\"provider\":\"github\",\"immutable_id\":\"repository-1\",\"canonical_remote\":\"https://github.com/acme/wormhole\"},\"origin_digest\":\"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\"}\n"
	canonical, err := state.CanonicalJSON(setupPublicationBindingV1{
		SchemaVersion: 1, Kind: "setup_publication_binding", Repository: repository, OriginDigest: origin,
	})
	if err != nil || string(canonical) != wantJSON {
		t.Fatalf("binding preimage = (%q, %v), want (%q, nil)", canonical, err, wantJSON)
	}
	got, err := DigestPublicationBindingConstraint(repository, origin)
	if err != nil || got != want {
		t.Fatalf("DigestPublicationBindingConstraint() = (%q, %v), want (%q, nil)", got, err, want)
	}

	for _, test := range []struct {
		name       string
		repository types.RepositoryIdentity
		origin     state.Digest
	}{
		{name: "repository", repository: types.RepositoryIdentity{Provider: "github"}, origin: origin},
		{name: "origin", repository: repository, origin: "SHA256:" + state.Digest(strings.Repeat("a", 64))},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := DigestPublicationBindingConstraint(test.repository, test.origin); err == nil || got != "" {
				t.Fatalf("invalid constraint = (%q, %v), want zero and error", got, err)
			}
		})
	}

	otherRepository := repository
	otherRepository.ImmutableID = "repository-2"
	otherOrigin := state.Digest("sha256:" + strings.Repeat("b", 64))
	for _, input := range []struct {
		repository types.RepositoryIdentity
		origin     state.Digest
	}{{otherRepository, origin}, {repository, otherOrigin}} {
		digest, err := DigestPublicationBindingConstraint(input.repository, input.origin)
		if err != nil || digest == want {
			t.Fatalf("distinct binding did not produce a distinct digest: (%q, %v)", digest, err)
		}
	}
}

func TestParsePublicationOriginOutputBoundsAndAmbiguity(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "checkout")
	equivalent := []byte("https://github.com/Acme/Repo\ngit@github.com:Acme/Repo.git\n")
	got, err := parsePublicationOriginOutput(root, equivalent, 0, false)
	want := observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "github.com", Path: "Acme/Repo"}
	if err != nil || got != want {
		t.Fatalf("equivalent output = (%+v, %v), want (%+v, nil)", got, err, want)
	}

	missing, err := parsePublicationOriginOutput(root, nil, 2, true)
	if err != nil || missing != (observedOriginV1{SchemaVersion: 1, Kind: "missing"}) {
		t.Fatalf("missing output = (%+v, %v)", missing, err)
	}
	for name, output := range map[string][]byte{
		"exact URL count":    []byte(strings.Repeat("https://example.com/a/b\n", maxPublicationOriginURLs)),
		"exact entry bytes":  append([]byte("/"+strings.Repeat("a", maxPublicationOriginURLBytes-1)), '\n'),
		"exact stdout bytes": []byte(strings.Repeat("/"+strings.Repeat("a", 4094)+"\n", 4)),
	} {
		t.Run(name, func(t *testing.T) {
			if len(output) > maxPublicationOriginOutputBytes {
				t.Fatalf("invalid boundary fixture length %d", len(output))
			}
			if _, err := parsePublicationOriginOutput(root, output, 0, false); err != nil {
				t.Fatalf("exact boundary rejected: %v", err)
			}
		})
	}

	tests := []struct {
		name    string
		output  []byte
		status  int
		missing bool
	}{
		{name: "status two output", output: []byte("credential@example.invalid:path\n"), status: 2, missing: true},
		{name: "generic status two", status: 2},
		{name: "other failure", status: 1},
		{name: "missing terminal LF", output: []byte("https://example.com/a/b"), status: 0},
		{name: "empty entry", output: []byte("https://example.com/a/b\n\n"), status: 0},
		{name: "too many", output: []byte(strings.Repeat("https://example.com/a/b\n", maxPublicationOriginURLs+1)), status: 0},
		{name: "entry too large", output: []byte("/" + strings.Repeat("a", maxPublicationOriginURLBytes) + "\n"), status: 0},
		{name: "stdout too large", output: []byte(strings.Repeat("a", maxPublicationOriginOutputBytes+1)), status: 0},
		{name: "carriage return", output: []byte("https://example.com/a/b\r\n"), status: 0},
		{name: "NUL", output: []byte("https://example.com/a/\x00b\n"), status: 0},
		{name: "ambiguous", output: []byte("https://example.com/a/b\nhttps://example.com/a/c\n"), status: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := parsePublicationOriginOutput(root, test.output, test.status, test.missing); err == nil || got != (observedOriginV1{}) {
				t.Fatalf("parsePublicationOriginOutput() = (%+v, %v), want zero and error", got, err)
			}
		})
	}
}

func TestNormalizePublicationOriginRejectsMalformedWithoutCredentialLeak(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "checkout")
	tests := []string{
		"https://user:super-secret@example.com/acme/repo",
		"ssh://admin-secret@example.com/acme/repo",
		"admin-secret@example.com:acme/repo",
		"@example.com:acme/repo",
		"https://bad_host.example/acme/repo",
		"https://-bad.example/acme/repo",
		"https://bad-.example/acme/repo",
		"https://example..com/acme/repo",
		"https://éxample.com/acme/repo",
		"https://example.com/acme//repo",
		"https://example.com/acme/../repo",
		"https://example.com/acme/%72epo",
		"https://example.com/acme/repo?token=super-secret",
		"https://example.com/acme/repo#super-secret",
		"https://example.com/acme/repo#",
		"https://example.com:/acme/repo",
		"ftp://example.com/acme/repo",
		"file://example.com/super-secret",
		"file:///tmp/%73uper-secret",
		"file:///tmp/repo#",
		"relative\\noncanonical",
		"/srv//repo",
		"/srv/repo/",
		"/srv/./repo",
		"file:///srv//repo",
		"file:///srv/repo/",
		"file:///srv/./repo",
	}
	for _, raw := range tests {
		t.Run(fmt.Sprintf("case-%d", len(raw)), func(t *testing.T) {
			got, err := normalizePublicationOrigin(root, raw)
			if err == nil || got != (observedOriginV1{}) {
				t.Fatalf("normalize malformed = (%+v, %v), want zero and error", got, err)
			}
			if strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "admin-secret") {
				t.Fatalf("error leaked raw locator or credential: %v", err)
			}
		})
	}
}

func TestNormalizePublicationOriginExactHostGrammar(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "checkout")
	label63 := strings.Repeat("a", 63)
	host253 := strings.Join([]string{label63, label63, label63, strings.Repeat("a", 61)}, ".")
	for _, raw := range []string{
		"HTTPS://EXAMPLE.COM/Acme/Repo.git",
		"https://" + host253 + "/acme/repo",
		"https://xn--bcher-kva.example/acme/repo",
		"git@[2001:db8::1]:acme/repo",
		"123:acme/repo",
	} {
		if got, err := normalizePublicationOrigin(root, raw); err != nil || got.Kind != "network" {
			t.Errorf("normalize valid host %q = (%+v, %v)", raw, got, err)
		}
	}

	for _, raw := range []string{
		"https://" + strings.Repeat("a", 64) + ".example/acme/repo",
		"https://a." + host253 + "/acme/repo",
		"https://example.com../acme/repo",
		"https://127.000.000.001/acme/repo",
		"ssh://git@2001:db8::1/acme/repo",
		"ssh://git@example.com:00000/acme/repo",
		"https:example.com/acme/repo",
		"ssh:git@example.com/acme/repo",
	} {
		if got, err := normalizePublicationOrigin(root, raw); err == nil || got != (observedOriginV1{}) {
			t.Errorf("normalize invalid host %q = (%+v, %v), want zero and error", raw, got, err)
		}
	}
}

func TestInspectPublicationOriginMissingAndOfflineNetwork(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	missing, err := InspectPublicationOrigin(context.Background(), repository.root)
	if err != nil || missing != "sha256:aaf73d9f175b4871b3ba05456155e482bbe9e4c11238ed4ad5a53323e05fe083" {
		t.Fatalf("InspectPublicationOrigin(missing) = (%q, %v)", missing, err)
	}

	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(writer, "network access forbidden", http.StatusInternalServerError)
	}))
	defer server.Close()
	runGit(t, repository.root, "remote", "add", "origin", server.URL+"/Acme/Repo.git")
	digest, err := InspectPublicationOrigin(context.Background(), repository.root)
	if err != nil || digest == "" || digest == missing {
		t.Fatalf("InspectPublicationOrigin(network) = (%q, %v)", digest, err)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("origin inspection made %d network requests", got)
	}
}

func TestPublicationOriginRunnerPreservesStatusAndHidesStderr(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name        string
		stdout      string
		stderr      string
		status      int
		wantStatus  int
		wantMissing bool
		wantErr     bool
	}{
		{name: "missing", status: 2, stderr: "error: No such remote 'origin'\n", wantStatus: 2, wantMissing: true},
		{name: "generic status two", status: 2, stderr: "super-secret missing remote", wantStatus: 2},
		{name: "status two with output", status: 2, stdout: "bad@example.invalid:path\n", stderr: "super-secret", wantStatus: 2},
		{name: "other failure", status: 7, stderr: "super-secret", wantStatus: 7},
		{name: "exact stdout limit", stdout: strings.Repeat("a", maxPublicationOriginOutputBytes)},
		{name: "stdout overflow", stdout: strings.Repeat("a", maxPublicationOriginOutputBytes+1), wantErr: true},
		{name: "stderr overflow", stderr: strings.Repeat("super-secret", maxGitStderrBytes), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installPublicationGitStub(t, test.stdout, test.stderr, test.status)
			output, status, missing, err := readPublicationOriginGit(context.Background(), root)
			if test.wantErr {
				if err == nil || output != nil || status != 0 || missing {
					t.Fatalf("readPublicationOriginGit() = (%q, %d, %t, %v), want zero and error", output, status, missing, err)
				}
			} else if err != nil || string(output) != test.stdout || status != test.wantStatus || missing != test.wantMissing {
				t.Fatalf("readPublicationOriginGit() = (%q, %d, %t, %v)", output, status, missing, err)
			}
			if err != nil && strings.Contains(err.Error(), "super-secret") {
				t.Fatalf("runner error leaked stderr: %v", err)
			}
		})
	}
}

func TestPublicationOriginObserverRevalidatesRootAndCheckout(t *testing.T) {
	wantRoot := filepath.Join(string(filepath.Separator), "checkout")
	wantCheckout := types.CheckoutIdentity{CanonicalPath: wantRoot, Device: 1, Inode: 2}
	tests := []struct {
		name          string
		finalRoot     string
		finalCheckout types.CheckoutIdentity
	}{
		{name: "root changed", finalRoot: filepath.Join(string(filepath.Separator), "other"), finalCheckout: wantCheckout},
		{name: "checkout changed", finalRoot: wantRoot, finalCheckout: types.CheckoutIdentity{CanonicalPath: wantRoot, Device: 1, Inode: 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rootCalls := 0
			checkoutCalls := 0
			_, err := observePublicationOriginWithReaders(context.Background(), wantRoot, publicationOriginReaders{
				canonicalRoot: func(string) (string, error) {
					rootCalls++
					if rootCalls == 1 {
						return wantRoot, nil
					}
					return test.finalRoot, nil
				},
				checkoutIdentity: func(string) (types.CheckoutIdentity, error) {
					checkoutCalls++
					if checkoutCalls == 1 {
						return wantCheckout, nil
					}
					return test.finalCheckout, nil
				},
				validateGitRoot: func(context.Context, string) error { return nil },
				readOrigin: func(context.Context, string) ([]byte, int, bool, error) {
					return []byte("https://github.com/acme/repo\n"), 0, false, nil
				},
			})
			if err == nil || !errors.Is(err, ErrGitOriginChanged) {
				t.Fatalf("observePublicationOriginWithReaders race error = %v, want ErrGitOriginChanged", err)
			}
		})
	}
}

func TestPublicationOriginObserverDoesNotExposeRunnerErrors(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "checkout")
	checkout := types.CheckoutIdentity{CanonicalPath: root, Device: 1, Inode: 2}
	readCalled := false
	_, err := observePublicationOriginWithReaders(context.Background(), root, publicationOriginReaders{
		canonicalRoot:    func(string) (string, error) { return root, nil },
		checkoutIdentity: func(string) (types.CheckoutIdentity, error) { return checkout, nil },
		validateGitRoot:  func(context.Context, string) error { return nil },
		readOrigin: func(context.Context, string) ([]byte, int, bool, error) {
			readCalled = true
			return nil, 0, false, errors.New("credential super-secret")
		},
	})
	if !readCalled || err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("observer runner error = %v, want non-leaking error", err)
	}
}

func TestInspectPublicationOriginRequiresExactCanonicalRootAndHidesGitDiagnostics(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	subdirectory := filepath.Join(repository.root, "nested")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if digest, err := InspectPublicationOrigin(context.Background(), subdirectory); err == nil || digest != "" {
		t.Fatalf("repository subdirectory = (%q, %v), want zero and error", digest, err)
	}
	if digest, err := InspectPublicationOrigin(context.Background(), repository.root+string(filepath.Separator)+"."); err == nil || digest != "" {
		t.Fatalf("noncanonical root = (%q, %v), want zero and error", digest, err)
	}
	symlink := filepath.Join(t.TempDir(), "checkout-link")
	if err := os.Symlink(repository.root, symlink); err != nil {
		t.Fatal(err)
	}
	if digest, err := InspectPublicationOrigin(context.Background(), symlink); err == nil || digest != "" {
		t.Fatalf("symlink root = (%q, %v), want zero and error", digest, err)
	}

	installPublicationGitStub(t, "", "credential super-secret", 7)
	if digest, err := InspectPublicationOrigin(context.Background(), repository.root); err == nil || digest != "" || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("Git diagnostic failure = (%q, %v), want zero non-leaking error", digest, err)
	}
}

func TestReobservePublicationOriginDetectsOriginRace(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "checkout")
	checkout := types.CheckoutIdentity{CanonicalPath: root, Device: 1, Inode: 2}
	firstOrigin := observedOriginV1{SchemaVersion: 1, Kind: "network", Host: "example.com", Path: "acme/one"}
	firstDigest, err := digestObservedOrigin(firstOrigin)
	if err != nil {
		t.Fatal(err)
	}
	outside := publicationOriginObservation{root: root, checkout: checkout, origin: firstOrigin, digest: firstDigest}
	for _, test := range []struct {
		name string
		edit func(*publicationOriginObservation)
	}{
		{name: "root", edit: func(value *publicationOriginObservation) {
			value.root = filepath.Join(string(filepath.Separator), "other")
		}},
		{name: "checkout", edit: func(value *publicationOriginObservation) { value.checkout.Inode++ }},
		{name: "origin", edit: func(value *publicationOriginObservation) {
			value.origin.Path = "acme/two"
			value.digest, _ = digestObservedOrigin(value.origin)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			inside := outside
			test.edit(&inside)
			err := reobservePublicationOriginWithReader(context.Background(), outside, func(context.Context, string) (publicationOriginObservation, error) {
				return inside, nil
			})
			if err == nil || !errors.Is(err, ErrGitOriginChanged) {
				t.Fatalf("reobservePublicationOriginWithReader error = %v, want ErrGitOriginChanged", err)
			}
		})
	}

	corrupt := outside
	corrupt.digest = "sha256:" + state.Digest(strings.Repeat("f", 64))
	if err := reobservePublicationOriginWithReader(context.Background(), corrupt, func(context.Context, string) (publicationOriginObservation, error) {
		return corrupt, nil
	}); err == nil || !errors.Is(err, ErrGitOriginChanged) {
		t.Fatalf("corrupt outside observation error = %v, want ErrGitOriginChanged", err)
	}
}

func TestReobservePublicationOriginAcceptsStableRealObservation(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	runGit(t, repository.root, "remote", "add", "origin", "https://github.com/acme/wormhole")
	outside, err := observePublicationOrigin(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	if err := reobservePublicationOrigin(context.Background(), outside); err != nil {
		t.Fatalf("reobservePublicationOrigin(stable): %v", err)
	}
}

func TestPublicationOriginUsesHardenedGitCommand(t *testing.T) {
	repository := createGitRepository(t, "00000000-0000-4000-8000-000000000001")
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "git.log")
	wrapper := filepath.Join(bin, "git")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$WORMHOLE_ORIGIN_GIT_LOG\"\n" +
		"printf 'GIT_SSH_COMMAND=%s\\nGIT_ASKPASS=%s\\nHTTPS_PROXY=%s\\n' \"$GIT_SSH_COMMAND\" \"$GIT_ASKPASS\" \"$HTTPS_PROXY\" >> \"$WORMHOLE_ORIGIN_GIT_LOG\"\n" +
		"exec \"$WORMHOLE_REAL_GIT\" \"$@\"\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORMHOLE_ORIGIN_GIT_LOG", logPath)
	t.Setenv("WORMHOLE_REAL_GIT", realGit)
	t.Setenv("GIT_SSH_COMMAND", "ssh -i super-secret")
	t.Setenv("GIT_ASKPASS", "/tmp/super-secret")
	t.Setenv("HTTPS_PROXY", "http://super-secret.invalid")
	if _, err := InspectPublicationOrigin(context.Background(), repository.root); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(logData)
	for _, want := range []string{"--no-optional-locks", "protocol.ssh.allow=never", "rev-parse --path-format=absolute --show-toplevel", "remote get-url --all origin", "GIT_SSH_COMMAND=/bin/false", "GIT_ASKPASS=/bin/false", "HTTPS_PROXY="} {
		if !strings.Contains(logText, want) {
			t.Errorf("hardened Git invocation missing %q:\n%s", want, logText)
		}
	}
	if strings.Contains(logText, "super-secret") {
		t.Fatalf("hardened Git invocation retained credential environment:\n%s", logText)
	}
}

func installPublicationGitStub(t *testing.T, stdout, stderr string, status int) {
	t.Helper()
	bin := t.TempDir()
	wrapper := filepath.Join(bin, "git")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%b' %q\nprintf '%%b' %q >&2\nexit %d\n", stdout, stderr, status)
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}
