//go:build !linux

package main

import (
	"fmt"
	"os"
)

func requireLinuxRoot() error {
	return fmt.Errorf("the updater can apply releases only on Linux")
}

func acquireProcessLock(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("another update is already running")
	}
	return func() {
		_ = file.Close()
		_ = os.Remove(path)
	}, nil
}

func fileOwner(os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}

func setFileOwner(string, int, int) error {
	return nil
}

func openReadNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}

func syncDirectoryPlatform(string) error {
	return nil
}
