package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const staleLockRetryBackoff = 50 * time.Millisecond

type watcherLockPayload struct {
	PID       int    `json:"pid"`
	Folder    string `json:"folder"`
	StartedAt string `json:"started_at"`
}

type folderLockManager struct {
	basePath string

	mu   sync.Mutex
	held map[string]*os.File
}

func newFolderLockManager(storagePath string) (*folderLockManager, error) {
	basePath, err := resolveWatcherLockBasePath(storagePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return nil, fmt.Errorf("create watcher lock dir: %w", err)
	}
	return &folderLockManager{
		basePath: basePath,
		held:     map[string]*os.File{},
	}, nil
}

func (m *folderLockManager) Acquire(folder string) (bool, error) {
	normalized, err := normalizeWatchedFolder(folder)
	if err != nil {
		return false, err
	}

	m.mu.Lock()
	if _, ok := m.held[normalized]; ok {
		m.mu.Unlock()
		return false, nil
	}
	m.mu.Unlock()

	lockPath := m.lockPathForNormalized(normalized)
	acquired, file, err := m.tryAcquireLock(normalized, lockPath)
	if err != nil {
		return false, err
	}
	if acquired {
		m.mu.Lock()
		m.held[normalized] = file
		m.mu.Unlock()
		return true, nil
	}

	stale := m.isStaleLock(lockPath)
	if !stale {
		return false, nil
	}

	_ = os.Remove(lockPath)
	time.Sleep(staleLockRetryBackoff)

	acquired, file, err = m.tryAcquireLock(normalized, lockPath)
	if err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}

	m.mu.Lock()
	m.held[normalized] = file
	m.mu.Unlock()
	return true, nil
}

func (m *folderLockManager) Release(folder string) error {
	normalized, err := normalizeWatchedFolder(folder)
	if err != nil {
		return err
	}

	m.mu.Lock()
	file, held := m.held[normalized]
	if held {
		delete(m.held, normalized)
	}
	m.mu.Unlock()

	if !held || file == nil {
		return nil
	}

	lockPath := m.lockPathForNormalized(normalized)
	if err := unlockFile(file); err != nil {
		_ = file.Close()
		return fmt.Errorf("unlock watcher lock: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close watcher lock: %w", err)
	}

	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove watcher lock: %w", err)
	}
	return nil
}

func (m *folderLockManager) lockPath(folder string) (string, error) {
	normalized, err := normalizeWatchedFolder(folder)
	if err != nil {
		return "", err
	}
	return m.lockPathForNormalized(normalized), nil
}

func (m *folderLockManager) lockPathForNormalized(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	prefix := hex.EncodeToString(sum[:])[:12]
	return filepath.Join(m.basePath, "_watcher_"+prefix+".lock")
}

func (m *folderLockManager) tryAcquireLock(normalized, lockPath string) (bool, *os.File, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("create watcher lock file: %w", err)
	}

	payload := watcherLockPayload{
		PID:       os.Getpid(),
		Folder:    normalized,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		m.cleanupFailedLock(file, lockPath)
		return false, nil, fmt.Errorf("encode watcher lock payload: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		m.cleanupFailedLock(file, lockPath)
		return false, nil, fmt.Errorf("write watcher lock payload: %w", err)
	}
	if err := file.Sync(); err != nil {
		m.cleanupFailedLock(file, lockPath)
		return false, nil, fmt.Errorf("sync watcher lock payload: %w", err)
	}

	if err := lockFileNonBlocking(file); err != nil {
		m.cleanupFailedLock(file, lockPath)
		return false, nil, nil
	}

	return true, file, nil
}

func (m *folderLockManager) cleanupFailedLock(file *os.File, lockPath string) {
	if file != nil {
		_ = file.Close()
	}
	_ = os.Remove(lockPath)
}

func (m *folderLockManager) isStaleLock(lockPath string) bool {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return !os.IsNotExist(err)
	}

	var payload watcherLockPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return true
	}
	if payload.PID <= 0 {
		return true
	}
	return !isPIDAlive(payload.PID)
}

func normalizeWatchedFolder(folder string) (string, error) {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return "", errors.New("folder is required")
	}

	absolute, err := filepath.Abs(folder)
	if err != nil {
		return "", fmt.Errorf("resolve absolute folder path: %w", err)
	}
	resolved := absolute
	if evaluated, evalErr := filepath.EvalSymlinks(absolute); evalErr == nil && strings.TrimSpace(evaluated) != "" {
		resolved = evaluated
	}

	resolved = filepath.Clean(resolved)
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
	}
	return resolved, nil
}

func resolveWatcherLockBasePath(storagePath string) (string, error) {
	basePath := strings.TrimSpace(storagePath)
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		basePath = filepath.Join(home, ".code-index")
	}

	if strings.HasPrefix(basePath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for watcher lock path: %w", err)
		}
		switch {
		case basePath == "~":
			basePath = home
		case strings.HasPrefix(basePath, "~/") || strings.HasPrefix(basePath, "~\\"):
			basePath = filepath.Join(home, basePath[2:])
		default:
			return "", fmt.Errorf("unsupported tilde path: %s", basePath)
		}
	}

	return filepath.Clean(basePath), nil
}
