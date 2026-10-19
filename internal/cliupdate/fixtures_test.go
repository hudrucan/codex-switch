package cliupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
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
