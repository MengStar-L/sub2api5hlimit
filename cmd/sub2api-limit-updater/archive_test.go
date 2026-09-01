package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type tarTestEntry struct {
	name     string
	typeflag byte
	body     string
	linkname string
}

func TestExtractCandidates(t *testing.T) {
	t.Parallel()
	root := "sub2api5hlimit-v0.2.1-linux-amd64"
	archive := writeTestArchive(t, []tarTestEntry{
		{name: root + "/", typeflag: tar.TypeDir},
		{name: root + "/README.md", typeflag: tar.TypeReg, body: "docs"},
		{name: root + "/dist/sub2api-limit-portal-linux-amd64", typeflag: tar.TypeReg, body: "portal"},
		{name: root + "/dist/sub2api-limit-updater-linux-amd64", typeflag: tar.TypeReg, body: "updater"},
	})
	destination := filepath.Join(t.TempDir(), "extract")
	candidates, err := extractCandidates(archive, "v0.2.1", "amd64", destination, 1024)
	if err != nil {
		t.Fatalf("extract candidates: %v", err)
	}
	portal, _ := os.ReadFile(candidates.Portal)
	updater, _ := os.ReadFile(candidates.Updater)
	if string(portal) != "portal" || string(updater) != "updater" {
		t.Fatalf("unexpected extracted content: %q, %q", portal, updater)
	}
}

func TestExtractCandidatesRejectsUnsafeArchives(t *testing.T) {
	t.Parallel()
	root := "sub2api5hlimit-v0.2.1-linux-amd64"
	tests := map[string][]tarTestEntry{
		"traversal": {{name: root + "/../escape", typeflag: tar.TypeReg, body: "x"}},
		"symlink":   {{name: root + "/link", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}},
		"duplicate": {
			{name: root + "/dist/sub2api-limit-portal-linux-amd64", typeflag: tar.TypeReg, body: "one"},
			{name: root + "/dist/sub2api-limit-portal-linux-amd64", typeflag: tar.TypeReg, body: "two"},
		},
		"wrong root": {{name: "other/dist/sub2api-limit-portal-linux-amd64", typeflag: tar.TypeReg, body: "x"}},
	}
	for name, entries := range tests {
		entries := entries
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			archive := writeTestArchive(t, entries)
			_, err := extractCandidates(archive, "v0.2.1", "amd64", filepath.Join(t.TempDir(), "extract"), 1024)
			if err == nil {
				t.Fatal("unsafe archive unexpectedly accepted")
			}
		})
	}
}

func writeTestArchive(t *testing.T, entries []tarTestEntry) string {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Typeflag: entry.typeflag, Mode: 0755, Linkname: entry.linkname}
		if entry.typeflag == tar.TypeReg || entry.typeflag == tar.TypeRegA {
			header.Size = int64(len(entry.body))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if header.Size > 0 {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatalf("write tar body: %v", err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), strings.ReplaceAll(t.Name(), "/", "-")+".tar.gz")
	if err := os.WriteFile(path, buffer.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
