package cliupdate

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

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
