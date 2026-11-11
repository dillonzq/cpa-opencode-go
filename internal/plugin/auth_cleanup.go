package plugin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"

	"github.com/dillonzq/cpa-opencode-go/internal/config"
)

// maxCleanupRecordBytes bounds how much of a candidate file is read.
const maxCleanupRecordBytes = 1 << 20

// cleanupConfirmTimeout bounds how long cleanup waits for CPA's file watcher
// to report one credential as disabled.
var cleanupConfirmTimeout = 2 * time.Second

// rememberAuthDirs keeps the last exposed directories so a temporarily empty
// runtime listing cannot hide files when the last configured key is removed.
func (m *Manager) rememberAuthDirs(entries []pluginapi.HostAuthFileEntry) {
	dirs := []string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.RuntimeOnly || !filepath.IsAbs(entry.Path) {
			continue
		}
		dir := filepath.Dir(entry.Path)
		if !seen[dir] {
			seen[dir] = true
			dirs = append(dirs, dir)
		}
	}
	if len(dirs) > 0 {
		m.mu.Lock()
		m.authDirs = dirs
		m.mu.Unlock()
	}
}

// discoverCleanupFiles adds JSON files under the known auth directories,
// including subdirectories, that CPA's runtime list omits.
func (m *Manager) discoverCleanupFiles(ctx context.Context, entries []pluginapi.HostAuthFileEntry) ([]pluginapi.HostAuthFileEntry, error) {
	m.rememberAuthDirs(entries)
	m.mu.RLock()
	dirs := append([]string(nil), m.authDirs...)
	m.mu.RUnlock()
	seen := map[string]bool{}
	for _, entry := range entries {
		if filepath.IsAbs(entry.Path) {
			seen[filepath.Clean(entry.Path)] = true
		}
	}
	for _, dir := range dirs {
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, fmt.Errorf("open credential directory for discovery failed")
		}
		err = fs.WalkDir(root.FS(), ".", func(relative string, file fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return fmt.Errorf("credential discovery canceled")
			}
			if walkErr != nil {
				return fmt.Errorf("scan credential directory failed")
			}
			if !file.Type().IsRegular() || !strings.EqualFold(filepath.Ext(file.Name()), ".json") {
				return nil // WalkDir does not follow symbolic links.
			}
			path := filepath.Join(dir, filepath.FromSlash(relative))
			if !seen[path] {
				seen[path] = true
				entries = append(entries, pluginapi.HostAuthFileEntry{Name: file.Name(), Path: path, Source: "file"})
			}
			return nil
		})
		_ = root.Close()
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// cleanupAuthRecords removes plugin-created credential files whose key is no
// longer configured. Ownership comes from the physical record, not host IDs
// (which can be filenames): only records with our type and the exact ID we
// derive from their key are owned. Errors never include keys, host error
// bodies or file contents.
func (m *Manager) cleanupAuthRecords(ctx context.Context, cfg config.Config) error {
	if m.bridge == nil {
		return fmt.Errorf("host bridge unavailable")
	}
	if err := m.bridge.waitAuthWrite(ctx); err != nil {
		return err
	}
	entries, err := m.bridge.AuthList(ctx)
	if err != nil {
		return err
	}
	entries, err = m.discoverCleanupFiles(ctx, entries)
	if err != nil {
		return err
	}
	var failure error
	for _, entry := range entries {
		if entry.RuntimeOnly || entry.Provider != "" && entry.Provider != ProviderID || entry.Type != "" && entry.Type != ProviderID {
			continue
		}
		if err := m.cleanupAuthFile(ctx, cfg, entry); err != nil {
			failure = err
		}
	}
	return failure
}

func (m *Manager) cleanupAuthFile(ctx context.Context, cfg config.Config, entry pluginapi.HostAuthFileEntry) error {
	name := entry.Name
	if !filepath.IsAbs(entry.Path) || filepath.Base(entry.Path) != name || strings.ContainsAny(name, `/\`) || !strings.EqualFold(filepath.Ext(name), ".json") {
		return nil // No usable physical file, e.g. a runtime-only host listing.
	}
	root, err := os.OpenRoot(filepath.Dir(entry.Path))
	if err != nil {
		return fmt.Errorf("open credential directory failed")
	}
	defer root.Close()
	record, err := readCleanupRecord(root, name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read credential failed")
	}
	if record == nil || cfg.HasAPIKey(record.APIKey) {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("credential cleanup canceled")
	}
	// The v8.0.0 host save callback does not apply the disabled flag to its
	// runtime Auth. Write the file and let CPA's directory watcher parse it,
	// then require known runtime credentials to be explicitly disabled before
	// unlinking. Discovered disk-only files are also written disabled and
	// checked for runtime attachment before deletion.
	disabled, err := disableCleanupFile(root, name, record.ID)
	if err != nil {
		return err
	}
	// A discovered file may have been loaded since the original listing.
	if entry.AuthIndex == "" {
		latest, err := m.bridge.AuthList(ctx)
		if err != nil {
			return err
		}
		for _, current := range latest {
			if current.AuthIndex != "" && current.Path == entry.Path {
				entry.AuthIndex = current.AuthIndex
				break
			}
		}
	}
	if entry.AuthIndex != "" {
		if err := m.awaitDisabledAuth(ctx, entry); err != nil {
			return err
		}
	}
	// Move to a unique non-JSON name, then verify that the staged file is the
	// exact file disabled above. If another writer replaced it, even with an
	// equivalent record, preserve that file instead of unlinking it.
	staged := name + ".opencode-go-cleanup-" + rand.Text()
	if err := root.Rename(name, staged); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stage credential deletion failed")
	}
	stagedInfo, statErr := root.Lstat(staged)
	current, readErr := readCleanupRecord(root, staged)
	if statErr != nil || !os.SameFile(disabled, stagedInfo) || readErr != nil || current == nil || current.ID != record.ID || !current.Disabled {
		if err := restoreCleanupFile(root, staged, name); err != nil {
			return err
		}
		return fmt.Errorf("credential changed during cleanup; preserved")
	}
	if err := root.Remove(staged); err != nil {
		if err := restoreCleanupFile(root, staged, name); err != nil {
			return err
		}
		return fmt.Errorf("delete credential failed")
	}
	return nil
}

// restoreCleanupFile never renames back over a replacement file. On
// filesystems without hard links, it preserves the staged file for manual
// recovery rather than overwrite.
func restoreCleanupFile(root *os.Root, staged, name string) error {
	if err := root.Link(staged, name); err != nil {
		return fmt.Errorf("credential preserved in a *.opencode-go-cleanup-* file; restore it manually")
	}
	if err := root.Remove(staged); err != nil {
		return fmt.Errorf("credential restored; extra *.opencode-go-cleanup-* file remains")
	}
	return nil
}

// disableCleanupFile writes disabled: true into name and returns the identity
// of the file it wrote, so deletion can refuse any replacement.
func disableCleanupFile(root *os.Root, name, id string) (os.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("credential changed before disabling")
	}
	file, err := openCleanupRecord(root, name, os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("open credential for disabling failed")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("credential changed before disabling")
	}
	// Prove ownership again on the descriptor that will be written.
	previous, err := io.ReadAll(io.LimitReader(file, maxCleanupRecordBytes+1))
	if err != nil || len(previous) > maxCleanupRecordBytes {
		return nil, fmt.Errorf("credential changed before disabling")
	}
	if record := ownedCleanupRecord(previous); record == nil || record.ID != id {
		return nil, fmt.Errorf("credential changed before disabling")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(previous, &fields) != nil {
		return nil, fmt.Errorf("invalid credential")
	}
	fields["disabled"] = json.RawMessage("true")
	raw, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode disabled credential failed")
	}
	if _, err := file.WriteAt(raw, 0); err != nil {
		return nil, fmt.Errorf("write disabled credential failed")
	}
	if err := file.Truncate(int64(len(raw))); err != nil {
		return nil, fmt.Errorf("truncate disabled credential failed")
	}
	return opened, nil
}

// awaitDisabledAuth waits until CPA lists the physical file as disabled. An
// absent runtime entry does not count as confirmation.
func (m *Manager) awaitDisabledAuth(ctx context.Context, entry pluginapi.HostAuthFileEntry) error {
	ctx, cancel := context.WithTimeout(ctx, cleanupConfirmTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		entries, err := m.bridge.AuthList(ctx)
		if err != nil {
			return fmt.Errorf("confirm disabled credential failed")
		}
		active, confirmed := false, false
		for _, current := range entries {
			if current.AuthIndex == entry.AuthIndex || current.Path == entry.Path {
				if current.RuntimeOnly || current.Path != entry.Path {
					active = true
					continue
				}
				confirmed = true
				if !current.Disabled && current.Status != "disabled" {
					active = true
				}
			}
		}
		if confirmed && !active {
			return nil // A matching physical record was explicitly disabled.
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("credential disable not confirmed")
		case <-ticker.C:
		}
	}
}

// readCleanupRecord returns the owned record in name, or nil for links,
// non-regular files and oversized, foreign or malformed records. Root
// confines filesystem operations to the host-reported directory.
func readCleanupRecord(root *os.Root, name string) (*authRecord, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil
	}
	file, err := openCleanupRecord(root, name, os.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("credential changed while reading")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxCleanupRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxCleanupRecordBytes {
		return nil, nil
	}
	return ownedCleanupRecord(raw), nil
}

// ownedCleanupRecord accepts only records the plugin itself writes: our type
// and the exact ID derived from the stored key.
func ownedCleanupRecord(raw []byte) *authRecord {
	var record authRecord
	if json.Unmarshal(raw, &record) != nil || record.Type != ProviderID || record.APIKey == "" || record.ID != credentialID(record.APIKey) {
		return nil
	}
	return &record
}
