package cliupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"testing"
)

func TestExtractTarGzip(t *testing.T) {
	var encoded bytes.Buffer
	gzipWriter := gzip.NewWriter(&encoded)
	tarWriter := tar.NewWriter(gzipWriter)
	want := []byte("binary")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "codex-switch", Mode: 0o755, Size: int64(len(want)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractTarGzip(encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("unexpected binary")
	}
}

func TestExtractZip(t *testing.T) {
	var encoded bytes.Buffer
	writer := zip.NewWriter(&encoded)
	entry, err := writer.Create("codex-switch.exe")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("binary")
	if _, err := entry.Write(want); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := extractZip(encoded.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("unexpected binary")
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

func TestReadBinaryLimit(t *testing.T) {
	if _, err := readBinary(io.LimitReader(zeroReader{}, maxBinaryBytes+1)); err == nil {
		t.Fatal("oversized executable accepted")
	}
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) { clear(data); return len(data), nil }
