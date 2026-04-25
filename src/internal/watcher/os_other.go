//go:build !unix

package watcher

import "os"

func lockFileNonBlocking(_ *os.File) error {
	return nil
}

func unlockFile(_ *os.File) error {
	return nil
}

func isPIDAlive(pid int) bool {
	return pid > 0
}
