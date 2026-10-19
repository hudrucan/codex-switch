package cliupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// TrustedRepository identifies binary distribution independently of the module path.
const TrustedRepository = "hudrucan/codex-switch"

// Provenance is a release-build policy marker, not a publisher signature.
// Empty fields identify source/development builds and disable mutating updates.
type Provenance struct {
	Repository string
	Channel    string
}

type transportFunc func(*http.Request) (*http.Response, error)

func (transport transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func releaseClient(transport http.RoundTripper) *http.Client {
	if transport == nil {
		// Do not inherit proxy authentication or mutable global HTTP state.
		transport = &http.Transport{TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second}
	}
	guard := transportFunc(func(request *http.Request) (*http.Response, error) {
		allowed := request.URL.String() == latestReleaseURL || validDownloadURL(request.URL)
		if request.Response != nil && validCDNURL(request.URL) {
			allowed = true
		}
		if !allowed || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
			return nil, errors.New("untrusted release request")
		}
		return transport.RoundTrip(request)
	})
	return &http.Client{Timeout: 45 * time.Second, Transport: guard, CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if len(via) != 1 || !validDownloadURL(via[0].URL) || !validCDNURL(request.URL) {
			return errors.New("untrusted release redirect")
		}
		request.Header.Del("Authorization")
		request.Header.Del("Cookie")
		return nil
	}}
}

func validCDNURL(value *url.URL) bool {
	return value.Scheme == "https" && value.Host == "release-assets.githubusercontent.com" && value.User == nil && value.Fragment == "" && value.Opaque == "" && value.RawPath == "" && strings.HasPrefix(value.Path, "/github-production-release-asset/") && !strings.Contains(value.Path, "..")
}

func validDownloadURL(value *url.URL) bool {
	if value.Scheme != "https" || value.Host != "github.com" || value.User != nil || value.RawQuery != "" || value.Fragment != "" || value.Opaque != "" || value.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(value.Path, "/"), "/")
	return len(parts) == 6 && strings.Join(parts[:4], "/") == TrustedRepository+"/releases/download" && validTag(parts[4]) && safeAssetName(parts[5])
}

func validTag(tag string) bool {
	return len(tag) <= 64 && semver.IsValid(tag) && semver.Canonical(tag) == tag && semver.Prerelease(tag) == ""
}

func normalizeVersion(version string) string {
	if !strings.HasPrefix(version, "v") {
		return "v" + version
	}
	return version
}

func supportedPlatform(goos, goarch string) bool {
	return (goos == "darwin" || goos == "linux" || goos == "windows") && (goarch == "amd64" || goarch == "arm64")
}

func safeAssetName(name string) bool {
	if name == "" || len(name) > 200 || strings.Contains(name, "..") {
		return false
	}
	for _, char := range name {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '_' || char == '-'
		if !allowed {
			return false
		}
	}
	return true
}

func validateRelease(latest release) error {
	if !validTag(latest.TagName) || latest.HTMLURL != "https://github.com/"+TrustedRepository+"/releases/tag/"+latest.TagName || latest.Draft || latest.Prerelease || len(latest.Assets) == 0 || len(latest.Assets) > 256 {
		return errors.New("invalid stable release metadata")
	}
	seen := map[string]bool{}
	for _, candidate := range latest.Assets {
		if !safeAssetName(candidate.Name) || seen[candidate.Name] || candidate.Size <= 0 || candidate.Size > maxDownloadBytes || candidate.BrowserDownloadURL != "https://github.com/"+TrustedRepository+"/releases/download/"+latest.TagName+"/"+candidate.Name {
			return errors.New("invalid or ambiguous release assets")
		}
		seen[candidate.Name] = true
	}
	return nil
}

// Reject duplicate keys (including nested keys) instead of JSON's last-value wins.
func uniqueJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if uniqueValue(decoder, 0) != nil {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}

func uniqueValue(decoder *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("metadata nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			name = strings.ToLower(name)
			if !ok || seen[name] {
				return errors.New("ambiguous metadata")
			}
			seen[name] = true
		}
		if err := uniqueValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
