package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type extractedCandidates struct {
	Portal  string
	Updater string
}

func extractCandidates(archivePath, tag, arch, destination string, maximumExpanded int64) (extractedCandidates, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return extractedCandidates{}, fmt.Errorf("open release archive: %w", err)
	}
	defer archive.Close()
	gzipReader, err := gzip.NewReader(archive)
	if err != nil {
		return extractedCandidates{}, fmt.Errorf("open gzip stream: %w", err)
	}
	defer gzipReader.Close()

	if err := os.MkdirAll(destination, 0700); err != nil {
		return extractedCandidates{}, fmt.Errorf("create extraction directory: %w", err)
	}
	root := fmt.Sprintf("sub2api5hlimit-%s-linux-%s", tag, arch)
	required := map[string]string{
		root + "/dist/sub2api-limit-portal-linux-" + arch:  filepath.Join(destination, "sub2api-limit-portal"),
		root + "/dist/sub2api-limit-updater-linux-" + arch: filepath.Join(destination, "sub2api-limit-updater"),
	}
	found := make(map[string]bool, len(required))
	seen := make(map[string]struct{})
	var expanded int64
	entries := 0
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return extractedCandidates{}, fmt.Errorf("read tar archive: %w", err)
		}
		entries++
		if entries > 4096 {
			return extractedCandidates{}, fmt.Errorf("release archive contains too many entries")
		}
		name, err := validateArchiveName(header.Name, root)
		if err != nil {
			return extractedCandidates{}, err
		}
		if _, exists := seen[name]; exists {
			return extractedCandidates{}, fmt.Errorf("release archive contains duplicate entry %q", name)
		}
		seen[name] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maximumExpanded-expanded {
				return extractedCandidates{}, fmt.Errorf("release archive exceeds the expanded-size limit")
			}
			expanded += header.Size
		default:
			return extractedCandidates{}, fmt.Errorf("release archive contains unsupported entry type for %q", name)
		}

		target, wanted := required[name]
		if !wanted {
			if _, err := io.CopyN(io.Discard, reader, header.Size); err != nil {
				return extractedCandidates{}, fmt.Errorf("read archive entry %q: %w", name, err)
			}
			continue
		}
		if err := writeCandidate(target, reader, header.Size); err != nil {
			return extractedCandidates{}, err
		}
		found[name] = true
	}
	for name := range required {
		if !found[name] {
			return extractedCandidates{}, fmt.Errorf("release archive is missing required binary %q", name)
		}
	}
	return extractedCandidates{
		Portal:  required[root+"/dist/sub2api-limit-portal-linux-"+arch],
		Updater: required[root+"/dist/sub2api-limit-updater-linux-"+arch],
	}, nil
}

func validateArchiveName(raw, root string) (string, error) {
	if raw == "" || strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("release archive contains an unsafe path")
	}
	trimmed := strings.TrimSuffix(raw, "/")
	for _, component := range strings.Split(trimmed, "/") {
		if component == "" || component == "." || component == ".." {
			return "", fmt.Errorf("release archive contains an unsafe path")
		}
	}
	clean := path.Clean(raw)
	if clean != trimmed {
		return "", fmt.Errorf("release archive contains a non-canonical path")
	}
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("release archive contains an unsafe path")
	}
	if clean != root && !strings.HasPrefix(clean, root+"/") {
		return "", fmt.Errorf("release archive entry is outside the expected package root")
	}
	return clean, nil
}

func writeCandidate(target string, source io.Reader, size int64) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return fmt.Errorf("create extracted binary: %w", err)
	}
	succeeded := false
	defer func() {
		file.Close()
		if !succeeded {
			os.Remove(target)
		}
	}()
	written, err := io.CopyN(file, source, size)
	if err != nil || written != size {
		return fmt.Errorf("extract binary: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync extracted binary: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close extracted binary: %w", err)
	}
	succeeded = true
	return nil
}
