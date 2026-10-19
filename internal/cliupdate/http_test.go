package cliupdate

import (
	"bytes"
	"net/url"
	"testing"
)

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

func TestDownloadRejectsOversizedBody(t *testing.T) {
	f := newUpdateFixture(t)
	checksum := f.asset("checksums.txt")
	parsed, _ := url.Parse(checksum.BrowserDownloadURL)
	f.bodies[parsed.Path] = bytes.Repeat([]byte("x"), maxChecksumBytes+1)
	if _, err := f.run("1.0.0", false, trustedBuild); err == nil || f.applied != 0 {
		t.Fatal("oversized download reached replacement")
	}
}
