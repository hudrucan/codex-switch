package cliupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/SilkageNet/codex-switch/internal/atomicfile"
	"github.com/SilkageNet/codex-switch/internal/filelock"
)

var trustedBuild = Provenance{Repository: TrustedRepository, Channel: "release"}

type updateFixture struct {
	release   release
	metadata  []byte
	bodies    map[string][]byte
	redirects map[string]string
	status    int
	applied   int
	binary    []byte
	client    *http.Client
	mu        sync.Mutex
	paths     []string
}

func tarFixture(t *testing.T, headers ...*tar.Header) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	writer := tar.NewWriter(gz)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > maxBinaryBytes {
			break
		}
		if header.Size > 0 {
			if _, err := writer.Write(bytes.Repeat([]byte("x"), int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	// An oversized header deliberately has no body: validation must reject it first.
	_ = writer.Close()
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func archiveFixture(t *testing.T) []byte {
	return tarFixture(t, &tar.Header{Name: "codex-switch", Mode: 0o755, Size: 6, Typeflag: tar.TypeReg})
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	archive := archiveFixture(t)
	name := archiveName("v1.2.3", "linux", "amd64")
	checksum := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), name))
	fixture := &updateFixture{status: 200, bodies: map[string][]byte{}, redirects: map[string]string{}}
	fixture.release = release{TagName: "v1.2.3", HTMLURL: "https://github.com/" + TrustedRepository + "/releases/tag/v1.2.3"}
	for filename, body := range map[string][]byte{name: archive, "checksums.txt": checksum} {
		address := "https://github.com/" + TrustedRepository + "/releases/download/v1.2.3/" + filename
		fixture.release.Assets = append(fixture.release.Assets, asset{Name: filename, BrowserDownloadURL: address, Size: int64(len(body))})
		parsed, _ := url.Parse(address)
		fixture.bodies[parsed.Path] = body
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.paths = append(fixture.paths, r.URL.Path)
		fixture.mu.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("updater sent authentication")
		}
		if target, ok := fixture.redirects[r.URL.Path]; ok {
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if fixture.status != 200 {
			w.WriteHeader(fixture.status)
			_, _ = io.WriteString(w, "SYNTHETIC_REMOTE_SECRET")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/repos/") {
			if fixture.metadata != nil {
				_, _ = w.Write(fixture.metadata)
			} else {
				_ = json.NewEncoder(w).Encode(fixture.release)
			}
			return
		}
		body, ok := fixture.bodies[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	local, _ := url.Parse(server.URL)
	fixture.client = releaseClient(transportFunc(func(request *http.Request) (*http.Response, error) {
		copy := request.Clone(request.Context())
		address := *request.URL
		address.Host = local.Host
		copy.URL = &address
		return server.Client().Transport.RoundTrip(copy)
	}))
	return fixture
}

func (fixture *updateFixture) requestCount() int {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	return len(fixture.paths)
}

func (fixture *updateFixture) run(installed string, check bool, build Provenance) (Result, error) {
	return run(context.Background(), installed, check, build, fixture.client, "linux", "amd64", func(binary []byte) error {
		fixture.applied++
		fixture.binary = append([]byte(nil), binary...)
		return nil
	})
}

func (fixture *updateFixture) asset(name string) *asset {
	for index := range fixture.release.Assets {
		if fixture.release.Assets[index].Name == name {
			return &fixture.release.Assets[index]
		}
	}
	panic("missing synthetic asset")
}

func TestVerifiedUpdateAndCheck(t *testing.T) {
	for _, scenario := range []string{"update", "check", "current", "newer"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newUpdateFixture(t)
			installed := "1.0.0"
			if scenario == "current" {
				installed = "1.2.3"
			}
			if scenario == "newer" {
				installed = "1.3.0"
			}
			result, err := fixture.run(installed, scenario == "check", trustedBuild)
			if err != nil {
				t.Fatal(err)
			}
			expected := scenario == "update"
			if result.Updated != expected || (fixture.applied == 1) != expected || result.Latest != "v1.2.3" || result.Current != (scenario == "current" || scenario == "newer") {
				t.Fatal("unexpected update/check behavior")
			}
			if expected && string(fixture.binary) != "xxxxxx" {
				t.Fatal("wrong verified executable")
			}
			if !expected && fixture.requestCount() != 1 {
				t.Fatal("check/current downloaded installation assets")
			}
		})
	}
}

func TestUnknownProvenanceNeverRequestsOrReplaces(t *testing.T) {
	for _, build := range []Provenance{{}, {Repository: TrustedRepository, Channel: "development"}, {Repository: "SilkageNet/codex-switch", Channel: "release"}, {Repository: TrustedRepository, Channel: "unknown"}} {
		fixture := newUpdateFixture(t)
		if _, err := fixture.run("1.0.0", false, build); err == nil || fixture.applied != 0 || fixture.requestCount() != 0 {
			t.Fatal("unknown/wrong provenance reached update")
		}
		if _, err := fixture.run("dev", true, build); err != nil {
			t.Fatal("read-only check should remain available")
		}
	}
}

func TestReleaseFailuresNeverReachReplacement(t *testing.T) {
	changes := map[string]func(*updateFixture){
		"invalid-json":       func(f *updateFixture) { f.metadata = []byte(`{"tag_name":`) },
		"duplicate-metadata": func(f *updateFixture) { f.metadata = []byte(`{"tag_name":"v1.2.3","tag_name":"v9.9.9"}`) },
		"oversized-metadata": func(f *updateFixture) { f.metadata = bytes.Repeat([]byte("x"), maxMetadataBytes+1) },
		"invalid-version":    func(f *updateFixture) { f.release.TagName = "v1.2" },
		"empty-version":      func(f *updateFixture) { f.release.TagName = "" },
		"prerelease":         func(f *updateFixture) { f.release.Prerelease = true },
		"draft":              func(f *updateFixture) { f.release.Draft = true },
		"upstream-origin": func(f *updateFixture) {
			f.release.HTMLURL = "https://github.com/SilkageNet/codex-switch/releases/tag/v1.2.3"
		},
		"upstream-asset": func(f *updateFixture) {
			f.asset("checksums.txt").BrowserDownloadURL = "https://github.com/SilkageNet/codex-switch/releases/download/v1.2.3/checksums.txt"
		},
		"wrong-platform": func(f *updateFixture) {
			f.asset(archiveName("v1.2.3", "linux", "amd64")).Name = "wrong-platform.tar.gz"
		},
		"missing-archive":  func(f *updateFixture) { f.release.Assets = []asset{*f.asset("checksums.txt")} },
		"missing-manifest": func(f *updateFixture) { f.release.Assets = []asset{*f.asset(archiveName("v1.2.3", "linux", "amd64"))} },
		"duplicate-archive": func(f *updateFixture) {
			f.release.Assets = append(f.release.Assets, *f.asset(archiveName("v1.2.3", "linux", "amd64")))
		},
		"duplicate-manifest": func(f *updateFixture) { f.release.Assets = append(f.release.Assets, *f.asset("checksums.txt")) },
		"oversized-archive":  func(f *updateFixture) { f.asset(archiveName("v1.2.3", "linux", "amd64")).Size = maxDownloadBytes + 1 },
		"oversized-manifest": func(f *updateFixture) { f.asset("checksums.txt").Size = maxChecksumBytes + 1 },
		"wrong-size":         func(f *updateFixture) { f.asset("checksums.txt").Size++ },
		"remote-body":        func(f *updateFixture) { f.status = 503 },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			f := newUpdateFixture(t)
			change(f)
			_, err := f.run("1.0.0", false, trustedBuild)
			if err == nil || f.applied != 0 || strings.Contains(err.Error(), "SYNTHETIC_REMOTE_SECRET") {
				t.Fatal("invalid release reached replacement or leaked diagnostics")
			}
		})
	}
	for name, manifest := range map[string]string{"missing": "", "malformed": "bad checksum", "duplicate": "duplicate", "incorrect": "incorrect"} {
		t.Run("checksum-"+name, func(t *testing.T) {
			f := newUpdateFixture(t)
			a := f.asset("checksums.txt")
			parsed, _ := url.Parse(a.BrowserDownloadURL)
			body := []byte(manifest)
			if name == "duplicate" {
				body = append(append([]byte(nil), f.bodies[parsed.Path]...), f.bodies[parsed.Path]...)
			}
			if name == "incorrect" {
				body = []byte(fmt.Sprintf("%064x  %s\n", 0, archiveName("v1.2.3", "linux", "amd64")))
			}
			f.bodies[parsed.Path] = body
			a.Size = int64(len(body))
			_, err := f.run("1.0.0", false, trustedBuild)
			if err == nil || f.applied != 0 {
				t.Fatal("invalid checksum reached replacement")
			}
		})
	}
}

func TestReleaseHTTPTrustBoundary(t *testing.T) {
	for _, address := range []string{"http://github.com/" + TrustedRepository, "https://evil.example/file", "https://github.com:443/" + TrustedRepository + "/releases/download/v1.2.3/checksums.txt", "https://user:secret@github.com/" + TrustedRepository + "/releases/download/v1.2.3/checksums.txt", "https://release-assets.githubusercontent.com/github-production-release-asset/a/b"} {
		f := newUpdateFixture(t)
		response, err := f.client.Get(address)
		if response != nil {
			_ = response.Body.Close()
		}
		if err == nil || f.requestCount() != 0 {
			t.Fatal("untrusted initial URL reached HTTP transport")
		}
	}
	for _, address := range []string{"https://evil.example/file", "http://release-assets.githubusercontent.com/github-production-release-asset/a/b", "https://github.com/SilkageNet/codex-switch/releases/download/v1.2.3/checksums.txt", "https://release-assets.githubusercontent.com:443/github-production-release-asset/a/b"} {
		f := newUpdateFixture(t)
		a := f.asset("checksums.txt")
		parsed, _ := url.Parse(a.BrowserDownloadURL)
		f.redirects[parsed.Path] = address
		if _, err := f.run("1.0.0", false, trustedBuild); err == nil || f.applied != 0 {
			t.Fatal("unsafe redirect was accepted")
		}
	}
	f := newUpdateFixture(t)
	a := f.asset("checksums.txt")
	parsed, _ := url.Parse(a.BrowserDownloadURL)
	cdn := "https://release-assets.githubusercontent.com/github-production-release-asset/123/456?signature=synthetic"
	f.redirects[parsed.Path] = cdn
	f.bodies["/github-production-release-asset/123/456"] = f.bodies[parsed.Path]
	if _, err := f.run("1.0.0", false, trustedBuild); err != nil || f.applied != 1 {
		t.Fatal("official single-hop release CDN flow failed")
	}
	f = newUpdateFixture(t)
	f.redirects["/repos/"+TrustedRepository+"/releases/latest"] = cdn
	if _, err := f.run("1.0.0", false, trustedBuild); err == nil {
		t.Fatal("metadata redirect was accepted")
	}
}

func TestTarRejectsAmbiguousAndSpecialExecutables(t *testing.T) {
	regular := func(name string) *tar.Header {
		return &tar.Header{Name: name, Mode: 0o755, Size: 6, Typeflag: tar.TypeReg}
	}
	for name, headers := range map[string][]*tar.Header{
		"nested": {regular("nested/codex-switch")}, "traversal": {regular("../codex-switch")}, "duplicate": {regular("codex-switch"), regular("codex-switch")}, "case-duplicate": {regular("codex-switch"), regular("CODEX-SWITCH")}, "missing": {regular("LICENSE")},
		"symlink": {{Name: "codex-switch", Linkname: "elsewhere", Typeflag: tar.TypeSymlink}}, "hardlink": {{Name: "codex-switch", Linkname: "elsewhere", Typeflag: tar.TypeLink}}, "fifo": {{Name: "codex-switch", Typeflag: tar.TypeFifo}}, "oversized": {{Name: "codex-switch", Size: maxBinaryBytes + 1, Typeflag: tar.TypeReg}},
		"special-after-valid": {regular("codex-switch"), {Name: "unexpected", Typeflag: tar.TypeFifo}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := extractTarGzip(tarFixture(t, headers...)); err == nil {
				t.Fatal("unsafe tar accepted")
			}
		})
	}
}

func TestZipRejectsAmbiguousAndSpecialExecutables(t *testing.T) {
	for _, scenario := range []string{"nested", "duplicate", "missing", "symlink", "special-after-valid"} {
		t.Run(scenario, func(t *testing.T) {
			var encoded bytes.Buffer
			writer := zip.NewWriter(&encoded)
			names := []string{"codex-switch.exe"}
			switch scenario {
			case "nested":
				names = []string{"nested/codex-switch.exe"}
			case "duplicate":
				names = append(names, names[0])
			case "missing":
				names = []string{"LICENSE"}
			case "special-after-valid":
				names = append(names, "unexpected")
			}
			for index, name := range names {
				header := &zip.FileHeader{Name: name}
				header.SetMode(0o755)
				if scenario == "symlink" || (scenario == "special-after-valid" && index == 1) {
					header.SetMode(os.ModeSymlink | 0o777)
				}
				entry, err := writer.CreateHeader(header)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = entry.Write([]byte("synthetic"))
			}
			_ = writer.Close()
			if _, err := extractZip(encoded.Bytes()); err == nil {
				t.Fatal("unsafe zip accepted")
			}
		})
	}
}

func TestReplacementPreservesExistingExecutableOnFailure(t *testing.T) {
	for _, stage := range []string{"success", "before-write", "after-write", "locked", "symlink", "parent-symlink", "lock-symlink"} {
		t.Run(stage, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "codex-switch")
			old := []byte("synthetic-old-binary")
			next := []byte("synthetic-new-binary")
			if err := os.WriteFile(target, old, 0o755); err != nil {
				t.Fatal(err)
			}
			actual := target
			if stage == "lock-symlink" {
				if err := os.Symlink(target, target+".update.lock"); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			if stage == "symlink" {
				actual = filepath.Join(root, "link")
				if err := os.Symlink(target, actual); err != nil {
					t.Skip("symlinks unavailable")
				}
			}
			if stage == "parent-symlink" {
				link := filepath.Join(root, "parent")
				if err := os.Symlink(root, link); err != nil {
					t.Skip("symlinks unavailable")
				}
				actual = filepath.Join(link, "codex-switch")
			}
			if stage == "locked" {
				lock, err := filelock.Acquire(target + ".update.lock")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = lock.Close() }()
			}
			write := func(path string, data []byte, mode os.FileMode) error {
				if stage == "before-write" {
					return &atomicfile.WriteError{Err: errors.New("synthetic path secret"), Replacement: atomicfile.ReplacementNotAttempted}
				}
				if err := atomicfile.Write(path, data, mode); err != nil {
					return err
				}
				if stage == "after-write" {
					return &atomicfile.WriteError{Err: errors.New("synthetic path secret"), Replacement: atomicfile.ReplacementCompleted}
				}
				return nil
			}
			err = replaceTarget(actual, next, write)
			data, readErr := os.ReadFile(target)
			if readErr != nil || (err == nil) != (stage == "success") || (err != nil && strings.Contains(err.Error(), "synthetic path secret")) {
				t.Fatal("unsafe replacement outcome")
			}
			expected := old
			if stage == "success" || stage == "after-write" {
				expected = next
			}
			if !bytes.Equal(data, expected) {
				t.Fatal("failed replacement lost the usable executable")
			}
			info, statErr := os.Stat(target)
			if statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o755) {
				t.Fatal("replacement did not preserve executable permissions")
			}
		})
	}
}

func TestInvalidVerifiedArchiveNeverReachesReplacement(t *testing.T) {
	for _, scenario := range []string{"nested", "duplicate", "special", "oversized-binary", "missing", "gzip-corrupt", "gzip-concatenated"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newUpdateFixture(t)
			name := archiveName("v1.2.3", "linux", "amd64")
			header := &tar.Header{Name: "codex-switch", Mode: 0o755, Size: 6, Typeflag: tar.TypeReg}
			headers := []*tar.Header{header}
			switch scenario {
			case "nested":
				header.Name = "nested/codex-switch"
			case "duplicate":
				headers = append(headers, header)
			case "special":
				headers = append(headers, &tar.Header{Name: "special", Typeflag: tar.TypeSymlink, Linkname: "elsewhere"})
			case "oversized-binary":
				header.Size = maxBinaryBytes + 1
			case "missing":
				header.Name = "LICENSE"
			}
			archive := tarFixture(t, headers...)
			if scenario == "gzip-corrupt" {
				archive[len(archive)-1] ^= 1
			}
			if scenario == "gzip-concatenated" {
				archive = append(archive, archive...)
			}
			manifest := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), name))
			for filename, body := range map[string][]byte{name: archive, "checksums.txt": manifest} {
				entry := fixture.asset(filename)
				parsed, _ := url.Parse(entry.BrowserDownloadURL)
				fixture.bodies[parsed.Path] = body
				entry.Size = int64(len(body))
			}
			if _, err := fixture.run("1.0.0", false, trustedBuild); err == nil || fixture.applied != 0 {
				t.Fatal("checksum-verified unsafe archive reached replacement")
			}
		})
	}
}

func TestSizeLimitsAndAmbiguousMetadata(t *testing.T) {
	for _, raw := range []string{`{"tag_name":"v1.2.3","TAG_NAME":"v9.9.9"}`, `{"assets":[{"name":"a","Name":"b"}]}`} {
		if uniqueJSON([]byte(raw)) {
			t.Fatal("case-insensitive duplicate field was accepted")
		}
	}
	for _, tag := range []string{"v01.2.3", "v1.2.3+build", "v1.2.3-rc1", "v1.2", "", strings.Repeat("x", 100)} {
		if validTag(tag) {
			t.Fatal("ambiguous version accepted")
		}
	}
	f := newUpdateFixture(t)
	checksum := f.asset("checksums.txt")
	parsed, _ := url.Parse(checksum.BrowserDownloadURL)
	f.bodies[parsed.Path] = bytes.Repeat([]byte("x"), maxChecksumBytes+1)
	if _, err := f.run("1.0.0", false, trustedBuild); err == nil || f.applied != 0 {
		t.Fatal("oversized download reached replacement")
	}
	if _, err := readBinary(io.LimitReader(zeroReader{}, maxBinaryBytes+1)); err == nil {
		t.Fatal("oversized executable accepted")
	}
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) { clear(data); return len(data), nil }
