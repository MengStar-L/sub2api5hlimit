package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManifestIsStrict(t *testing.T) {
	t.Parallel()
	valid := `{"schema":1,"version":"0.2.1","min_updater_version":"0.2.0","mode":"binary"}`
	manifest, err := parseManifest([]byte(valid))
	if err != nil || manifest.Mode != "binary" {
		t.Fatalf("parse valid manifest: %#v, %v", manifest, err)
	}
	for _, invalid := range []string{
		`{"schema":1,"version":"0.2.1","min_updater_version":"0.2.0","mode":"automatic"}`,
		`{"schema":1,"version":"0.2.1","min_updater_version":"0.2.0","mode":"binary","asset":"x"}`,
		`{"schema":1,"version":"0.2.1+rebuilt","min_updater_version":"0.2.0","mode":"binary"}`,
		valid + `{}`,
	} {
		if _, err := parseManifest([]byte(invalid)); err == nil {
			t.Fatalf("invalid manifest unexpectedly accepted: %s", invalid)
		}
	}
}

func TestChecksumsRejectUnsafeAndDuplicateNames(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	for _, body := range []string{
		digest + "  ../asset.tar.gz\n",
		digest + "  asset.tar.gz\n" + digest + "  asset.tar.gz\n",
		"not-a-checksum\n",
	} {
		if _, err := parseChecksums([]byte(body)); err == nil {
			t.Fatalf("invalid checksum file unexpectedly accepted: %q", body)
		}
	}
}

func TestPrepareUpdateVerifiesReleaseAssets(t *testing.T) {
	t.Parallel()
	archiveName := "sub2api5hlimit-v0.2.1-linux-amd64.tar.gz"
	archive := []byte("verified archive bytes")
	archiveDigest := digestOf(archive)
	manifest := []byte(`{"schema":1,"version":"0.2.1","min_updater_version":"0.2.0","mode":"binary"}`)
	checksums := []byte(fmt.Sprintf("%s  %s\n", archiveDigest, archiveName))

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		assets := []githubAsset{
			assetFor(server.URL, manifestAssetName, manifest),
			assetFor(server.URL, checksumsAssetName, checksums),
			assetFor(server.URL, archiveName, archive),
		}
		switch request.URL.Path {
		case "/latest":
			_ = json.NewEncoder(writer).Encode(githubRelease{TagName: "v0.2.1", Assets: assets})
		case "/" + manifestAssetName:
			_, _ = writer.Write(manifest)
		case "/" + checksumsAssetName:
			_, _ = writer.Write(checksums)
		case "/" + archiveName:
			_, _ = writer.Write(archive)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	updateDir := t.TempDir()
	cfg := productionConfig()
	cfg.LatestURL = server.URL + "/latest"
	cfg.HTTPClient = server.Client()
	cfg.AllowHTTP = true
	cfg.AllowedHost = func(string) bool { return true }
	cfg.UpdateDir = updateDir
	cfg.GOOS = "linux"
	cfg.GOARCH = "amd64"
	var phases []string
	prepared, err := prepareUpdate(context.Background(), cfg, "0.2.0", "v0.2.1", func(phase string) error {
		phases = append(phases, phase)
		return nil
	})
	if err != nil {
		t.Fatalf("prepare update: %v", err)
	}
	defer os.Remove(prepared.ArchivePath)
	if prepared.Version != "0.2.1" || prepared.ArchiveName != archiveName {
		t.Fatalf("unexpected prepared update: %#v", prepared)
	}
	downloaded, err := os.ReadFile(prepared.ArchivePath)
	if err != nil || !reflect.DeepEqual(downloaded, archive) {
		t.Fatalf("downloaded archive mismatch: %v", err)
	}
	wantPhases := []string{"checking", "downloading", "verifying", "downloading", "verifying", "downloading", "verifying"}
	if !reflect.DeepEqual(phases, wantPhases) {
		t.Fatalf("phases = %#v, want %#v", phases, wantPhases)
	}
	if filepath.Dir(prepared.ArchivePath) != updateDir {
		t.Fatalf("archive was written outside update directory: %s", prepared.ArchivePath)
	}
}

func TestPrepareUpdateRejectsRequestedVersionDrift(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(githubRelease{TagName: "v0.2.2"})
	}))
	defer server.Close()
	cfg := productionConfig()
	cfg.LatestURL = server.URL
	cfg.HTTPClient = server.Client()
	cfg.AllowHTTP = true
	cfg.AllowedHost = func(string) bool { return true }
	cfg.GOOS = "linux"
	cfg.GOARCH = "amd64"
	_, err := prepareUpdate(context.Background(), cfg, "0.2.0", "v0.2.1", func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "no longer") {
		t.Fatalf("version drift error = %v", err)
	}
}

func TestPrepareUpdateRejectsBuildMetadataReleaseIdentity(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewEncoder(writer).Encode(githubRelease{TagName: "v0.2.1+rebuilt"})
	}))
	defer server.Close()
	cfg := productionConfig()
	cfg.LatestURL = server.URL
	cfg.HTTPClient = server.Client()
	cfg.AllowHTTP = true
	cfg.AllowedHost = func(string) bool { return true }
	cfg.GOOS = "linux"
	cfg.GOARCH = "amd64"
	_, err := prepareUpdate(context.Background(), cfg, "0.2.0", "v0.2.1", func(string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "build metadata") {
		t.Fatalf("build metadata release error = %v", err)
	}
}

func assetFor(baseURL, name string, body []byte) githubAsset {
	return githubAsset{
		Name:               name,
		Size:               int64(len(body)),
		Digest:             "sha256:" + digestOf(body),
		BrowserDownloadURL: baseURL + "/" + name,
	}
}

func digestOf(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
