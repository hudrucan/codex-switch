package cliupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"

	"golang.org/x/mod/semver"
)

const (
	latestReleaseURL = "https://api.github.com/repos/" + TrustedRepository + "/releases/latest"
	maxMetadataBytes = 2 << 20
	maxChecksumBytes = 1 << 20
	maxDownloadBytes = 128 << 20
	maxBinaryBytes   = 64 << 20
)

type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type release struct {
	TagName    string  `json:"tag_name"`
	HTMLURL    string  `json:"html_url"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []asset `json:"assets"`
}

type Result struct {
	Installed string `json:"installed"`
	Latest    string `json:"latest"`
	Current   bool   `json:"current"`
	Updated   bool   `json:"updated"`
}

func Run(ctx context.Context, installed string, checkOnly bool, provenance ...Provenance) (Result, error) {
	var build Provenance
	if len(provenance) == 1 {
		build = provenance[0]
	}
	return run(ctx, installed, checkOnly, build, releaseClient(nil), runtime.GOOS, runtime.GOARCH, replaceExecutable)
}

func run(ctx context.Context, installed string, checkOnly bool, build Provenance, client *http.Client, goos, goarch string, apply func([]byte) error) (Result, error) {
	if !checkOnly && (build.Repository != TrustedRepository || build.Channel != "release" || !validTag(normalizeVersion(installed))) {
		return Result{}, errors.New("self-update requires a trusted fork release build; source or unknown-provenance builds must be installed manually")
	}
	latest, err := fetchRelease(ctx, client)
	if err != nil {
		return Result{}, err
	}
	result := Result{Installed: installed, Latest: latest.TagName}
	if isCurrent(installed, latest.TagName) {
		result.Current = true
		return result, nil
	}
	if checkOnly {
		return result, nil
	}

	if !supportedPlatform(goos, goarch) {
		return Result{}, errors.New("unsupported release platform")
	}
	archiveName := archiveName(latest.TagName, goos, goarch)
	archiveAsset, err := findAsset(latest, archiveName)
	if err != nil {
		return Result{}, err
	}
	checksumAsset, err := findAsset(latest, "checksums.txt")
	if err != nil {
		return Result{}, err
	}
	checksums, err := download(ctx, client, checksumAsset)
	if err != nil {
		return Result{}, fmt.Errorf("download checksums: %w", err)
	}
	expected, err := checksumFor(checksums, archiveName)
	if err != nil {
		return Result{}, err
	}
	archive, err := download(ctx, client, archiveAsset)
	if err != nil {
		return Result{}, fmt.Errorf("download release archive: %w", err)
	}
	actual := sha256.Sum256(archive)
	if !bytes.Equal(actual[:], expected) {
		return Result{}, errors.New("release archive checksum did not match checksums.txt")
	}
	binary, err := extractBinary(archiveName, archive)
	if err != nil {
		return Result{}, err
	}
	if err := apply(binary); err != nil {
		return Result{}, err
	}
	result.Updated = true
	return result, nil
}

func fetchRelease(ctx context.Context, client *http.Client) (release, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "codex-switch")
	response, err := client.Do(request)
	if err != nil {
		return release{}, errors.New("release metadata request failed (diagnostics redacted)")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("release metadata returned HTTP %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, maxMetadataBytes+1))
	if err != nil {
		return release{}, errors.New("release metadata could not be read")
	}
	if len(encoded) > maxMetadataBytes {
		return release{}, errors.New("latest release metadata is too large")
	}
	var latest release
	if !uniqueJSON(encoded) || json.Unmarshal(encoded, &latest) != nil {
		return release{}, errors.New("invalid release metadata")
	}
	if err := validateRelease(latest); err != nil {
		return release{}, err
	}
	return latest, nil
}

func isCurrent(installed, latest string) bool {
	current := normalizeVersion(installed)
	if !validTag(current) {
		return false
	}
	return semver.Compare(current, latest) >= 0
}

func archiveName(tag, goos, goarch string) string {
	version := strings.TrimPrefix(tag, "v")
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return fmt.Sprintf("codex-switch_%s_%s_%s%s", version, goos, goarch, extension)
}

func findAsset(latest release, name string) (asset, error) {
	var result asset
	count := 0
	for _, candidate := range latest.Assets {
		if candidate.Name == name {
			result = candidate
			count++
		}
	}
	if count != 1 {
		return asset{}, errors.New("required release asset is missing or duplicated")
	}
	return result, nil
}

func download(ctx context.Context, client *http.Client, source asset) ([]byte, error) {
	limit := int64(maxDownloadBytes)
	if source.Name == "checksums.txt" {
		limit = maxChecksumBytes
	}
	if source.Size <= 0 || source.Size > limit {
		return nil, errors.New("release asset has an invalid size")
	}
	parsed, err := url.Parse(source.BrowserDownloadURL)
	if err != nil || !validDownloadURL(parsed) {
		return nil, errors.New("release asset has an untrusted URL")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.BrowserDownloadURL, nil)
	if err != nil {
		return nil, errors.New("invalid release request")
	}
	request.Header.Set("User-Agent", "codex-switch")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("release download failed (diagnostics redacted)")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download returned HTTP %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, errors.New("release download could not be read")
	}
	if int64(len(encoded)) != source.Size || int64(len(encoded)) > limit {
		return nil, errors.New("release asset size disagrees with metadata or exceeds its limit")
	}
	return encoded, nil
}

func checksumFor(checksums []byte, name string) ([]byte, error) {
	if len(checksums) > maxChecksumBytes {
		return nil, errors.New("checksum manifest is too large")
	}
	var result []byte
	seen := map[string]bool{}
	for _, line := range strings.Split(string(checksums), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, errors.New("malformed checksum manifest")
		}
		filename := strings.TrimPrefix(fields[1], "*")
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || !safeAssetName(filename) || seen[filename] {
			return nil, errors.New("invalid or ambiguous checksum manifest")
		}
		seen[filename] = true
		if filename == name {
			result = decoded
		}
	}
	if result == nil {
		return nil, errors.New("required archive checksum is missing")
	}
	return result, nil
}

func extractBinary(name string, archive []byte) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		return extractZip(archive)
	}
	if strings.HasSuffix(name, ".tar.gz") {
		return extractTarGzip(archive)
	}
	return nil, errors.New("unsupported release archive")
}

func extractZip(encoded []byte) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(encoded), int64(len(encoded)))
	if err != nil {
		return nil, errors.New("invalid release zip")
	}
	state := archiveState{expected: "codex-switch.exe", seen: map[string]bool{}}
	for _, entry := range reader.File {
		if !entry.Mode().IsRegular() && !entry.Mode().IsDir() {
			return nil, errors.New("special archive entry rejected")
		}
		if entry.UncompressedSize64 > maxDownloadBytes {
			return nil, errors.New("archive entry too large")
		}
		if err := state.check(entry.Name, entry.Mode().IsDir(), int64(entry.UncompressedSize64)); err != nil {
			return nil, err
		}
		if entry.Mode().IsDir() {
			continue
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, errors.New("unreadable zip entry")
		}
		data, readErr := readBinary(stream)
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) != int64(entry.UncompressedSize64) {
			return nil, errors.New("invalid zip entry data")
		}
		if entry.Name == state.expected {
			state.binary = data
		}
	}
	return state.finish()
}

func extractTarGzip(encoded []byte) ([]byte, error) {
	compressed := bytes.NewReader(encoded)
	gzipReader, err := gzip.NewReader(compressed)
	if err != nil {
		return nil, errors.New("invalid release gzip")
	}
	gzipReader.Multistream(false)
	defer func() { _ = gzipReader.Close() }()
	expanded := &io.LimitedReader{R: gzipReader, N: maxDownloadBytes + (1 << 20) + 1}
	tarReader := tar.NewReader(expanded)
	state := archiveState{expected: "codex-switch", seen: map[string]bool{}}
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errors.New("invalid release tar")
		}
		isDir := header.Typeflag == tar.TypeDir
		if !isDir && header.Typeflag != tar.TypeReg {
			return nil, errors.New("special archive entry rejected")
		}
		if header.Format != tar.FormatUSTAR || len(header.PAXRecords) != 0 {
			return nil, errors.New("extended archive entries rejected")
		}
		if err := state.check(header.Name, isDir, header.Size); err != nil {
			return nil, err
		}
		if isDir {
			continue
		}
		data, err := readBinary(tarReader)
		if err != nil || int64(len(data)) != header.Size {
			return nil, errors.New("invalid tar entry data")
		}
		if header.Name == state.expected {
			state.binary = data
		}
	}
	padding, err := io.ReadAll(expanded)
	if err != nil || expanded.N == 0 || compressed.Len() != 0 || len(bytes.Trim(padding, "\x00")) != 0 {
		return nil, errors.New("invalid gzip trailer or trailing archive data")
	}
	return state.finish()
}

func readBinary(reader io.Reader) ([]byte, error) {
	binary, err := io.ReadAll(io.LimitReader(reader, maxBinaryBytes+1))
	if err != nil {
		return nil, errors.New("unreadable release executable")
	}
	if len(binary) == 0 || len(binary) > maxBinaryBytes {
		return nil, errors.New("release executable has an invalid size")
	}
	return binary, nil
}
