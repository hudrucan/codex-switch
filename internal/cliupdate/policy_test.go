package cliupdate

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestArchiveName(t *testing.T) {
	if got := archiveName("v1.2.3", "darwin", "arm64"); got != "codex-switch_1.2.3_darwin_arm64.tar.gz" {
		t.Fatalf("unexpected archive name: %s", got)
	}
	if got := archiveName("v1.2.3", "windows", "amd64"); got != "codex-switch_1.2.3_windows_amd64.zip" {
		t.Fatalf("unexpected archive name: %s", got)
	}
}

func TestIsCurrent(t *testing.T) {
	if !isCurrent("1.2.3", "v1.2.3") || !isCurrent("v1.3.0", "v1.2.3") || isCurrent("dev", "v1.2.3") {
		t.Fatal("unexpected version comparison")
	}
}

func TestChecksumFor(t *testing.T) {
	payload := []byte("archive")
	sum := sha256.Sum256(payload)
	encoded := []byte(fmt.Sprintf("%x  codex-switch.tar.gz\n", sum))
	got, err := checksumFor(encoded, "codex-switch.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sum[:]) {
		t.Fatal("unexpected checksum")
	}
}

func TestReleaseMetadataCanonical(t *testing.T) {
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
}
