package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type savedFile struct {
	Original string
	Backup   string
	Existed  bool
	Mode     os.FileMode
	UID      int
	GID      int
	HasOwner bool
}

func validateManagedPaths(cfg updaterConfig) error {
	if !filepath.IsAbs(cfg.InstallRoot) {
		return fmt.Errorf("installation root must be absolute")
	}
	for _, managed := range []string{cfg.BinDir, cfg.DataDir, cfg.BackupDir, cfg.UpdateDir} {
		if !pathWithin(cfg.InstallRoot, managed) {
			return fmt.Errorf("managed path is outside the installation root")
		}
		info, err := os.Lstat(managed)
		if err != nil {
			return fmt.Errorf("inspect managed directory: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed path is not a real directory: %s", managed)
		}
	}
	for _, managed := range []string{cfg.PortalPath, cfg.UpdaterPath, cfg.Database, cfg.EnvPath, cfg.RequestPath, cfg.StatusPath, cfg.JournalPath, cfg.LockPath} {
		if !pathWithin(cfg.InstallRoot, managed) {
			return fmt.Errorf("managed file path is outside the installation root")
		}
	}
	for _, required := range []string{cfg.PortalPath, cfg.UpdaterPath, cfg.EnvPath} {
		if err := validateRegularFile(required, true); err != nil {
			return err
		}
	}
	for _, optional := range []string{cfg.Database, cfg.Database + "-wal", cfg.Database + "-shm", cfg.RequestPath, cfg.StatusPath, cfg.JournalPath, cfg.LockPath} {
		if err := validateRegularFile(optional, false); err != nil {
			return err
		}
	}
	return nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || relative == "." || filepath.IsAbs(relative) {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateRegularFile(path string, required bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !required {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect managed file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("managed file is not a regular file: %s", path)
	}
	return nil
}

func saveFile(ctx context.Context, original, backup string) (savedFile, error) {
	entry := savedFile{Original: original, Backup: backup}
	info, err := os.Lstat(original)
	if os.IsNotExist(err) {
		return entry, nil
	}
	if err != nil {
		return entry, fmt.Errorf("inspect file for backup: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return entry, fmt.Errorf("refusing to back up non-regular file: %s", original)
	}
	entry.Existed = true
	entry.Mode = info.Mode().Perm()
	entry.UID, entry.GID, entry.HasOwner = fileOwner(info)
	if err := copyFile(ctx, original, backup, entry.Mode, entry.UID, entry.GID, entry.HasOwner); err != nil {
		return entry, err
	}
	return entry, nil
}

func restoreFile(ctx context.Context, entry savedFile) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !entry.Existed {
		if err := os.Remove(entry.Original); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove newly created file during rollback: %w", err)
		}
		return syncDirectory(filepath.Dir(entry.Original))
	}
	return atomicCopyContext(ctx, entry.Backup, entry.Original, entry.Mode, entry.UID, entry.GID, entry.HasOwner)
}

func atomicInstall(source, destination string, mode os.FileMode) error {
	info, err := os.Lstat(destination)
	if err != nil {
		return fmt.Errorf("inspect replacement target: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("replacement target is not a regular file")
	}
	uid, gid, hasOwner := fileOwner(info)
	return atomicCopy(source, destination, mode, uid, gid, hasOwner)
}

func atomicCopy(source, destination string, mode os.FileMode, uid, gid int, preserveOwner bool) error {
	return atomicCopyContext(context.Background(), source, destination, mode, uid, gid, preserveOwner)
}

func atomicCopyContext(ctx context.Context, source, destination string, mode os.FileMode, uid, gid int, preserveOwner bool) error {
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".replace-*.tmp")
	if err != nil {
		return fmt.Errorf("create replacement file: %w", err)
	}
	temporaryPath := temporary.Name()
	succeeded := false
	defer func() {
		_ = temporary.Close()
		if !succeeded {
			_ = os.Remove(temporaryPath)
		}
	}()
	sourceFile, err := openReadNoFollow(source)
	if err != nil {
		return fmt.Errorf("open replacement source: %w", err)
	}
	copyErr := copyWithContext(ctx, temporary, sourceFile)
	closeSourceErr := sourceFile.Close()
	if copyErr != nil {
		return fmt.Errorf("copy replacement file: %w", copyErr)
	}
	if closeSourceErr != nil {
		return fmt.Errorf("close replacement source: %w", closeSourceErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set replacement mode: %w", err)
	}
	if preserveOwner {
		if err := setFileOwner(temporaryPath, uid, gid); err != nil {
			return fmt.Errorf("set replacement owner: %w", err)
		}
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync replacement file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close replacement file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("replace file atomically: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	succeeded = true
	return nil
}

func copyFile(ctx context.Context, source, destination string, mode os.FileMode, uid, gid int, preserveOwner bool) error {
	input, err := openReadNoFollow(source)
	if err != nil {
		return fmt.Errorf("open backup source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	succeeded := false
	defer func() {
		_ = output.Close()
		if !succeeded {
			_ = os.Remove(destination)
		}
	}()
	if err := copyWithContext(ctx, output, input); err != nil {
		return fmt.Errorf("copy backup file: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if preserveOwner {
		if err := setFileOwner(destination, uid, gid); err != nil {
			return fmt.Errorf("preserve backup owner: %w", err)
		}
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync backup file: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close backup file: %w", err)
	}
	succeeded = true
	return nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			written, writeErr := destination.Write(buffer[:count])
			if writeErr != nil {
				return writeErr
			}
			if written != count {
				return io.ErrShortWrite
			}
		}
		switch {
		case readErr == io.EOF:
			return nil
		case readErr != nil:
			return readErr
		case count == 0:
			return io.ErrNoProgress
		}
	}
}

func syncDirectory(path string) error {
	if err := syncDirectoryPlatform(path); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
