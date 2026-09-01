package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	manifestAssetName  = "update-manifest.json"
	checksumsAssetName = "SHA256SUMS"
)

type githubRelease struct {
	TagName    string        `json:"tag_name"`
	Draft      bool          `json:"draft"`
	Prerelease bool          `json:"prerelease"`
	Assets     []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type updateManifest struct {
	Schema            int    `json:"schema"`
	Version           string `json:"version"`
	MinUpdaterVersion string `json:"min_updater_version"`
	Mode              string `json:"mode"`
}

type preparedUpdate struct {
	Version     string
	Tag         string
	ArchiveName string
	ArchivePath string
}

type upToDateError struct{ version string }

func (err upToDateError) Error() string {
	return "already running the latest stable version " + err.version
}

type manualRequiredError struct{ reason string }

func (err manualRequiredError) Error() string { return err.reason }

func prepareUpdate(ctx context.Context, cfg updaterConfig, currentVersion, requestedTarget string, progress func(string) error) (preparedUpdate, error) {
	if currentVersion == "dev" {
		return preparedUpdate{}, manualRequiredError{reason: "development builds cannot perform automatic updates"}
	}
	current, err := parseSemVersion(currentVersion)
	if err != nil {
		return preparedUpdate{}, manualRequiredError{reason: "the installed updater has an invalid version"}
	}
	if cfg.GOOS != "linux" || (cfg.GOARCH != "amd64" && cfg.GOARCH != "arm64") {
		return preparedUpdate{}, manualRequiredError{reason: "automatic updates support only Linux amd64 and arm64"}
	}

	if err := progress("checking"); err != nil {
		return preparedUpdate{}, err
	}
	releaseBody, err := fetchBytes(ctx, cfg, cfg.LatestURL, cfg.MaxReleaseBytes)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("read latest GitHub Release: %w", err)
	}
	var release githubRelease
	if err := json.Unmarshal(releaseBody, &release); err != nil {
		return preparedUpdate{}, fmt.Errorf("decode latest GitHub Release: %w", err)
	}
	if release.Draft || release.Prerelease {
		return preparedUpdate{}, fmt.Errorf("latest GitHub Release is not stable")
	}
	if !strings.HasPrefix(release.TagName, "v") {
		return preparedUpdate{}, fmt.Errorf("latest release tag is not a v-prefixed semantic version")
	}
	if strings.Contains(release.TagName, "+") {
		return preparedUpdate{}, fmt.Errorf("latest release tag contains unsupported build metadata")
	}
	targetVersion := strings.TrimPrefix(release.TagName, "v")
	target, err := parseSemVersion(targetVersion)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("latest release tag: %w", err)
	}
	if len(target.pre) != 0 {
		return preparedUpdate{}, fmt.Errorf("latest release tag is not a stable semantic version")
	}
	requested, err := parseSemVersion(strings.TrimPrefix(requestedTarget, "v"))
	if err != nil || compareSemVersion(requested, target) != 0 || requestedTarget != release.TagName {
		return preparedUpdate{}, fmt.Errorf("requested target is no longer the latest stable release")
	}
	if compareSemVersion(target, current) <= 0 {
		return preparedUpdate{}, upToDateError{version: currentVersion}
	}

	assets, err := indexAssets(release.Assets)
	if err != nil {
		return preparedUpdate{}, err
	}
	manifestAsset, err := requireAsset(assets, manifestAssetName, cfg.MaxMetadata)
	if err != nil {
		return preparedUpdate{}, err
	}
	if err := progress("downloading"); err != nil {
		return preparedUpdate{}, err
	}
	manifestBody, err := fetchVerifiedAsset(ctx, cfg, manifestAsset, cfg.MaxMetadata)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("download update manifest: %w", err)
	}
	if err := progress("verifying"); err != nil {
		return preparedUpdate{}, err
	}
	manifest, err := parseManifest(manifestBody)
	if err != nil {
		return preparedUpdate{}, err
	}
	if manifest.Version != targetVersion {
		return preparedUpdate{}, fmt.Errorf("manifest version %q does not match release tag %q", manifest.Version, release.TagName)
	}
	minimum, err := parseSemVersion(manifest.MinUpdaterVersion)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("manifest minimum updater version: %w", err)
	}
	if manifest.Mode == "manual" {
		return preparedUpdate{}, manualRequiredError{reason: "this release changes the installation layout and requires the packaged installer"}
	}
	if compareSemVersion(current, minimum) < 0 {
		return preparedUpdate{}, manualRequiredError{reason: fmt.Sprintf("release %s requires updater %s or newer; run the packaged installer", release.TagName, manifest.MinUpdaterVersion)}
	}

	archiveName := fmt.Sprintf("sub2api5hlimit-%s-linux-%s.tar.gz", release.TagName, cfg.GOARCH)
	archiveAsset, err := requireAsset(assets, archiveName, cfg.MaxArchive)
	if err != nil {
		return preparedUpdate{}, err
	}
	checksumsAsset, err := requireAsset(assets, checksumsAssetName, cfg.MaxMetadata)
	if err != nil {
		return preparedUpdate{}, err
	}
	if err := progress("downloading"); err != nil {
		return preparedUpdate{}, err
	}
	checksumsBody, err := fetchVerifiedAsset(ctx, cfg, checksumsAsset, cfg.MaxMetadata)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("download release checksums: %w", err)
	}
	if err := progress("verifying"); err != nil {
		return preparedUpdate{}, err
	}
	checksums, err := parseChecksums(checksumsBody)
	if err != nil {
		return preparedUpdate{}, err
	}
	expectedChecksum, ok := checksums[archiveName]
	if !ok {
		return preparedUpdate{}, fmt.Errorf("SHA256SUMS does not contain %s", archiveName)
	}
	assetChecksum, err := parseGitHubDigest(archiveAsset.Digest)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("archive GitHub digest: %w", err)
	}
	if expectedChecksum != assetChecksum {
		return preparedUpdate{}, fmt.Errorf("archive digest differs between GitHub metadata and SHA256SUMS")
	}

	if err := progress("downloading"); err != nil {
		return preparedUpdate{}, err
	}
	archivePath, digest, size, err := downloadAsset(ctx, cfg, archiveAsset, cfg.MaxArchive)
	if err != nil {
		return preparedUpdate{}, fmt.Errorf("download release archive: %w", err)
	}
	if err := progress("verifying"); err != nil {
		os.Remove(archivePath)
		return preparedUpdate{}, err
	}
	if size != archiveAsset.Size {
		os.Remove(archivePath)
		return preparedUpdate{}, fmt.Errorf("archive size differs from GitHub metadata")
	}
	if digest != expectedChecksum {
		os.Remove(archivePath)
		return preparedUpdate{}, fmt.Errorf("archive SHA-256 verification failed")
	}
	return preparedUpdate{Version: targetVersion, Tag: release.TagName, ArchiveName: archiveName, ArchivePath: archivePath}, nil
}

func parseManifest(body []byte) (updateManifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var manifest updateManifest
	if err := decoder.Decode(&manifest); err != nil {
		return updateManifest{}, fmt.Errorf("decode update manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return updateManifest{}, fmt.Errorf("update manifest contains trailing data")
	}
	if manifest.Schema != 1 {
		return updateManifest{}, fmt.Errorf("unsupported update manifest schema %d", manifest.Schema)
	}
	if _, err := parseSemVersion(manifest.Version); err != nil {
		return updateManifest{}, fmt.Errorf("manifest version: %w", err)
	}
	if strings.Contains(manifest.Version, "+") {
		return updateManifest{}, fmt.Errorf("manifest version build metadata is not supported")
	}
	if _, err := parseSemVersion(manifest.MinUpdaterVersion); err != nil {
		return updateManifest{}, fmt.Errorf("manifest minimum updater version: %w", err)
	}
	if manifest.Mode != "binary" && manifest.Mode != "manual" {
		return updateManifest{}, fmt.Errorf("unsupported update manifest mode %q", manifest.Mode)
	}
	return manifest, nil
}

func indexAssets(list []githubAsset) (map[string]githubAsset, error) {
	assets := make(map[string]githubAsset, len(list))
	for _, asset := range list {
		if asset.Name == "" {
			return nil, fmt.Errorf("GitHub Release contains an unnamed asset")
		}
		if _, exists := assets[asset.Name]; exists {
			return nil, fmt.Errorf("GitHub Release contains duplicate asset %q", asset.Name)
		}
		assets[asset.Name] = asset
	}
	return assets, nil
}

func requireAsset(assets map[string]githubAsset, name string, maximum int64) (githubAsset, error) {
	asset, ok := assets[name]
	if !ok {
		return githubAsset{}, fmt.Errorf("GitHub Release is missing required asset %q", name)
	}
	if asset.Size <= 0 || asset.Size > maximum {
		return githubAsset{}, fmt.Errorf("asset %q has an invalid size", name)
	}
	if asset.BrowserDownloadURL == "" {
		return githubAsset{}, fmt.Errorf("asset %q has no download URL", name)
	}
	if _, err := parseGitHubDigest(asset.Digest); err != nil {
		return githubAsset{}, fmt.Errorf("asset %q: %w", name, err)
	}
	return asset, nil
}

func fetchVerifiedAsset(ctx context.Context, cfg updaterConfig, asset githubAsset, maximum int64) ([]byte, error) {
	body, err := fetchBytes(ctx, cfg, asset.BrowserDownloadURL, maximum)
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != asset.Size {
		return nil, fmt.Errorf("downloaded size differs from GitHub metadata")
	}
	expected, err := parseGitHubDigest(asset.Digest)
	if err != nil {
		return nil, err
	}
	actual := sha256.Sum256(body)
	if hex.EncodeToString(actual[:]) != expected {
		return nil, fmt.Errorf("GitHub asset SHA-256 verification failed")
	}
	return body, nil
}

func fetchBytes(ctx context.Context, cfg updaterConfig, rawURL string, maximum int64) ([]byte, error) {
	response, err := request(ctx, cfg, rawURL)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return nil, fmt.Errorf("response exceeds the download limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read HTTP response: %w", err)
	}
	if int64(len(body)) > maximum {
		return nil, fmt.Errorf("response exceeds the download limit")
	}
	return body, nil
}

func downloadAsset(ctx context.Context, cfg updaterConfig, asset githubAsset, maximum int64) (path string, digest string, size int64, err error) {
	response, err := request(ctx, cfg, asset.BrowserDownloadURL)
	if err != nil {
		return "", "", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", 0, fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maximum {
		return "", "", 0, fmt.Errorf("response exceeds the archive limit")
	}
	file, err := os.CreateTemp(cfg.UpdateDir, ".archive-*.tmp")
	if err != nil {
		return "", "", 0, fmt.Errorf("create archive file: %w", err)
	}
	path = file.Name()
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(path)
		}
	}()
	hasher := sha256.New()
	written, err := copyLimited(file, response.Body, hasher, maximum)
	if err != nil {
		return "", "", 0, err
	}
	if err := file.Sync(); err != nil {
		return "", "", 0, fmt.Errorf("sync archive file: %w", err)
	}
	return path, hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func copyLimited(destination io.Writer, source io.Reader, hasher hash.Hash, maximum int64) (int64, error) {
	limited := &io.LimitedReader{R: source, N: maximum + 1}
	written, err := io.Copy(io.MultiWriter(destination, hasher), limited)
	if err != nil {
		return 0, fmt.Errorf("read download: %w", err)
	}
	if written > maximum {
		return 0, fmt.Errorf("response exceeds the archive limit")
	}
	return written, nil
}

func request(ctx context.Context, cfg updaterConfig, rawURL string) (*http.Response, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return nil, fmt.Errorf("invalid download URL")
	}
	if parsed.Scheme != "https" && !(cfg.AllowHTTP && parsed.Scheme == "http") {
		return nil, fmt.Errorf("download URL must use HTTPS")
	}
	if cfg.AllowedHost != nil && !cfg.AllowedHost(parsed.Hostname()) {
		return nil, fmt.Errorf("download URL host is not approved")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.github+json, application/octet-stream")
	request.Header.Set("User-Agent", "sub2api-limit-updater")
	response, err := cfg.HTTPClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	return response, nil
}

func parseGitHubDigest(value string) (string, error) {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) {
		return "", fmt.Errorf("GitHub asset is missing a SHA-256 digest")
	}
	digest := strings.TrimPrefix(value, prefix)
	if len(digest) != sha256.Size*2 {
		return "", fmt.Errorf("GitHub asset has an invalid SHA-256 digest")
	}
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size {
		return "", fmt.Errorf("GitHub asset has an invalid SHA-256 digest")
	}
	return strings.ToLower(digest), nil
}

var checksumLinePattern = regexp.MustCompile(`^([0-9A-Fa-f]{64}) [ *](.+)$`)

func parseChecksums(body []byte) (map[string]string, error) {
	checksums := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		matches := checksumLinePattern.FindStringSubmatch(line)
		if matches == nil {
			return nil, fmt.Errorf("SHA256SUMS contains an invalid line")
		}
		name := matches[2]
		if filepath.Base(name) != name || strings.Contains(name, "\\") {
			return nil, fmt.Errorf("SHA256SUMS contains an unsafe asset name")
		}
		if _, exists := checksums[name]; exists {
			return nil, fmt.Errorf("SHA256SUMS contains duplicate asset %q", name)
		}
		checksums[name] = strings.ToLower(matches[1])
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SHA256SUMS: %w", err)
	}
	if len(checksums) == 0 {
		return nil, errors.New("SHA256SUMS is empty")
	}
	return checksums, nil
}
