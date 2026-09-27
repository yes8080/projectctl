// Package install installs the embedded skill and the current platform binary
// into an existing or empty project without Git, network or global changes.
package install

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const skillName = "github-project-control"
const manifestPath = ".project-control/manifest.json"

var agentRoots = map[string]string{
	"codex":   ".agents/skills/" + skillName,
	"claude":  ".claude/skills/" + skillName,
	"generic": ".project-control/skill",
}

var dataDirs = []string{
	".project-control/bin", ".project-control/sources", ".project-control/tasks",
	".project-control/events", ".project-control/packages", ".project-control/evidence",
	".project-control/decisions", ".project-control/constraints", ".project-control/adr",
}

// Options describes a project-local install. Assets must have SKILL.md at its
// root, and Executable is a binary for the current platform (default os.Executable).
type Options struct {
	Project    string
	Agents     []string
	DryRun     bool
	Upgrade    bool
	Executable string
	Assets     fs.FS
	Version    string
}

// UninstallOptions preserves project memory unless Purge and Backup are supplied.
type UninstallOptions struct {
	Project string
	DryRun  bool
	Purge   bool
	Backup  string
}

type record struct {
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	SchemaVersion  int               `json:"schema_version"`
	SkillName      string            `json:"skill_name"`
	InstallVersion string            `json:"install_version"`
	PackageSHA256  string            `json:"package_sha256"`
	Active         bool              `json:"active"`
	Agents         []string          `json:"agents"`
	Files          map[string]record `json:"files"`
	Directories    []string          `json:"directories"`
	InstalledAt    string            `json:"installed_at"`
}

type payloadFile struct {
	Kind string
	Data []byte
	Mode fs.FileMode
}

type treeEntry struct {
	Directory bool
	Hash      string
}

func digest(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func jsonBytes(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// canonicalProject resolves only the explicitly selected root, including the
// nearest existing ancestor for new projects. Child paths are never resolved.
func canonicalProject(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("project path is required")
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	var suffix []string
	candidate := abs
	for {
		_, err = os.Lstat(candidate)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("cannot resolve project root %q", abs)
		}
		suffix = append(suffix, filepath.Base(candidate))
		candidate = parent
	}
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	for i := len(suffix) - 1; i >= 0; i-- {
		real = filepath.Join(real, suffix[i])
	}
	if err := safePath(real); err != nil {
		return "", err
	}
	return real, nil
}

func safePath(value string) error {
	abs, err := filepath.Abs(value)
	if err != nil {
		return err
	}
	for p := abs; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink path is not allowed: %s", p)
			}
			if p != abs && !info.IsDir() {
				return fmt.Errorf("non-directory ancestor: %s", p)
			}
		}
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	return nil
}

func validRelative(name string) bool {
	if !fs.ValidPath(name) || name == "." || strings.ContainsAny(name, "\\:\x00\r\n") {
		return false
	}
	for _, segment := range strings.Split(name, "/") {
		if strings.HasSuffix(segment, ".") || strings.HasSuffix(segment, " ") {
			return false
		}
		device := strings.ToLower(strings.SplitN(segment, ".", 2)[0])
		if device == "con" || device == "prn" || device == "aux" || device == "nul" {
			return false
		}
		if len(device) == 4 && (strings.HasPrefix(device, "com") || strings.HasPrefix(device, "lpt")) && device[3] >= '1' && device[3] <= '9' {
			return false
		}
	}
	return true
}

func allowedFile(name, kind string) bool {
	if !validRelative(name) {
		return false
	}
	switch kind {
	case "runtime":
		return name == ".project-control/bin/projectctl" || name == ".project-control/bin/projectctl.exe"
	case "config":
		return name == ".project-control/config.json" || name == ".project-control/.gitignore"
	case "start":
		return name == ".project-control/START_HERE.md"
	case "skill":
		for _, root := range agentRoots {
			if strings.HasPrefix(name, root+"/") {
				return true
			}
		}
	}
	return false
}

func allowedDir(name string) bool {
	if !validRelative(name) {
		return false
	}
	for _, fixed := range append([]string{".project-control", ".agents", ".agents/skills", ".claude", ".claude/skills"}, dataDirs...) {
		if name == fixed {
			return true
		}
	}
	for _, root := range agentRoots {
		if name == root || strings.HasPrefix(name, root+"/") {
			return true
		}
	}
	return false
}

func full(root, relative string) string { return filepath.Join(root, filepath.FromSlash(relative)) }

func readRegular(name string) ([]byte, error) {
	if err := safePath(name); err != nil {
		return nil, err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("expected regular file: %s", name)
	}
	return os.ReadFile(name)
}

// hashRegular streams evidence and backups, which can be much larger than RAM.
func hashRegular(name string) (string, error) {
	if err := safePath(name); err != nil {
		return "", err
	}
	info, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("expected regular file: %s", name)
	}
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func exists(name string) (bool, error) {
	_, err := os.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func readManifest(root string) (*manifest, error) {
	name := full(root, manifestPath)
	data, err := readRegular(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 10*1024*1024 {
		return nil, errors.New("manifest exceeds size limit")
	}
	var result manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("invalid trailing manifest content")
	}
	if result.SchemaVersion != 1 || result.SkillName != skillName || result.InstallVersion == "" {
		return nil, errors.New("unsupported manifest schema or package")
	}
	if result.Files == nil || result.Directories == nil || len(result.Agents) == 0 || len(result.Files) > 50000 || len(result.Directories) > 50000 {
		return nil, errors.New("invalid manifest entries")
	}
	for _, agent := range result.Agents {
		if _, ok := agentRoots[agent]; !ok {
			return nil, errors.New("invalid manifest agent")
		}
	}
	hashPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for name, entry := range result.Files {
		if !allowedFile(name, entry.Kind) || !hashPattern.MatchString(entry.SHA256) {
			return nil, fmt.Errorf("unmanaged or invalid manifest file: %s", name)
		}
		if err := safePath(full(root, name)); err != nil {
			return nil, err
		}
	}
	for _, name := range result.Directories {
		if !allowedDir(name) {
			return nil, fmt.Errorf("unmanaged manifest directory: %s", name)
		}
		if err := safePath(full(root, name)); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

func sourceFiles(assets fs.FS) (map[string][]byte, error) {
	if assets == nil {
		return nil, errors.New("embedded skill assets are required")
	}
	result := make(map[string][]byte)
	err := fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		if !validRelative(name) {
			return fmt.Errorf("unsafe asset path: %s", name)
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink asset is not allowed: %s", name)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular asset: %s", name)
		}
		data, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		result[name] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	if _, ok := result["SKILL.md"]; !ok {
		return nil, errors.New("skill assets must contain SKILL.md")
	}
	return result, nil
}

func mkdir(name string) error {
	if err := safePath(name); err != nil {
		return err
	}
	if err := os.MkdirAll(name, 0755); err != nil {
		return err
	}
	return safePath(name)
}

func writeAtomic(name string, data []byte, mode fs.FileMode) error {
	if err := safePath(name); err != nil {
		return err
	}
	if err := mkdir(filepath.Dir(name)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(name), ".project-control-write-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if _, err = file.Write(data); err == nil {
		err = file.Chmod(mode)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := safePath(name); err != nil {
		return err
	}
	if err := os.Rename(temporary, name); err != nil {
		return fmt.Errorf("cannot replace %s (on Windows, stop processes using this file and run an external distribution binary): %w", name, err)
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func unique(values []string) []string {
	set := make(map[string]bool)
	for _, value := range values {
		set[value] = true
	}
	return sortedKeys(set)
}

func windowsRunningBinary(name string) error {
	if runtime.GOOS != "windows" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return err
	}
	targetInfo, err := os.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if os.SameFile(selfInfo, targetInfo) {
		return errors.New("cannot replace or remove the running Windows projectctl binary; run install/uninstall from an external distribution binary")
	}
	return nil
}

// Install preflights every conflict before writing. Configuration and project
// data survive upgrades. Inactive uninstall receipts allow in-place reinstallation.
func Install(opts Options) (map[string]any, error) {
	root, err := canonicalProject(opts.Project)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return nil, errors.New("project must be a directory")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	previous, err := readManifest(root)
	if err != nil {
		return nil, err
	}
	if present, err := exists(full(root, ".project-control")); err != nil {
		return nil, err
	} else if present && previous == nil {
		return nil, errors.New("unmanaged .project-control exists; back it up and move it before installation")
	}
	selected := append([]string{}, opts.Agents...)
	if previous != nil {
		selected = append(selected, previous.Agents...)
	}
	if len(selected) == 0 {
		selected = append(selected, "codex")
	}
	selected = unique(selected)
	for _, agent := range selected {
		if _, ok := agentRoots[agent]; !ok {
			return nil, fmt.Errorf("unknown agent %q; choose codex, claude or generic", agent)
		}
	}
	assets, err := sourceFiles(opts.Assets)
	if err != nil {
		return nil, err
	}
	executable := opts.Executable
	if executable == "" {
		executable, err = os.Executable()
		if err != nil {
			return nil, err
		}
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	binary, err := readRegular(executable)
	if err != nil {
		return nil, err
	}
	if len(binary) == 0 {
		return nil, errors.New("executable is empty")
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	binaryPath := ".project-control/bin/projectctl"
	if runtime.GOOS == "windows" {
		binaryPath += ".exe"
	}
	payload := map[string]payloadFile{binaryPath: {Kind: "runtime", Data: binary, Mode: 0755}}
	assetHashes := make(map[string]string)
	for name, data := range assets {
		assetHashes[name] = digest(data)
		for _, agent := range selected {
			payload[agentRoots[agent]+"/"+name] = payloadFile{Kind: "skill", Data: data, Mode: 0644}
		}
	}
	assetHashes["@runtime"] = digest(binary)
	packageData, _ := jsonBytes(assetHashes)
	config, _ := jsonBytes(map[string]any{
		"schema_version": 1, "project_name": filepath.Base(root), "max_attempts": 3,
		"context_max_chars": 60000, "github": map[string]any{"repository": nil},
		"autonomy": map[string]any{"remote_writes": false},
	})
	start := "# Project Control\n\nStart with `.project-control/bin/projectctl --help` (on Windows: `.project-control/bin/projectctl.exe --help`).\n\nExisting project: inspect current code, tests and design documents, then register versioned sources and unresolved requirements.\n\nEmpty project: establish goals, scope, constraints and acceptance criteria first. Installation does not approve a design.\n\nKeep task contracts, attempts, decisions and verification evidence in `.project-control/`. Use a fresh reviewer context. Remote writes default to disabled in `config.json`.\n\nUninstall with an external distribution binary: `projectctl uninstall --project PATH`. Project memory and an inactive manifest receipt remain for reinstallation. Full removal requires `--purge --backup /absolute/backup.zip`.\n"
	payload[".project-control/config.json"] = payloadFile{Kind: "config", Data: config, Mode: 0644}
	payload[".project-control/.gitignore"] = payloadFile{Kind: "config", Data: []byte("bin/\nruntime.lock\n.runtime.lock\nstate.json\n"), Mode: 0644}
	payload[".project-control/START_HERE.md"] = payloadFile{Kind: "start", Data: []byte(start), Mode: 0644}
	oldFiles := make(map[string]record)
	if previous != nil {
		oldFiles = previous.Files
	}
	for _, agent := range selected {
		prefix := agentRoots[agent]
		if err := safePath(full(root, prefix)); err != nil {
			return nil, err
		}
		present, err := exists(full(root, prefix))
		if err != nil {
			return nil, err
		}
		owned := false
		for name := range oldFiles {
			if strings.HasPrefix(name, prefix+"/") {
				owned = true
				break
			}
		}
		if present && !owned {
			return nil, fmt.Errorf("unmanaged skill directory exists: %s", prefix)
		}
	}
	if previous != nil && !opts.Upgrade {
		changed := previous.InstallVersion != version
		for name, entry := range oldFiles {
			if entry.Kind != "runtime" && entry.Kind != "skill" {
				continue
			}
			item, ok := payload[name]
			if !ok || digest(item.Data) != entry.SHA256 {
				changed = true
			}
		}
		for name, item := range payload {
			if item.Kind != "runtime" && item.Kind != "skill" {
				continue
			}
			entry, ok := oldFiles[name]
			if !ok || entry.SHA256 != digest(item.Data) {
				changed = true
			}
		}
		if changed {
			return nil, errors.New("package or agent integration changed; rerun with --upgrade")
		}
	}
	writes, removals, unchanged, preserved := []string{}, []string{}, []string{}, []string{}
	records := make(map[string]record)
	for _, name := range sortedKeys(oldFiles) {
		entry := oldFiles[name]
		target := full(root, name)
		present, err := exists(target)
		if err != nil {
			return nil, err
		}
		if present && (entry.Kind == "runtime" || entry.Kind == "skill") {
			data, err := readRegular(target)
			if err != nil {
				return nil, err
			}
			if digest(data) != entry.SHA256 {
				return nil, fmt.Errorf("locally modified managed file; installation aborted: %s", name)
			}
		}
		if _, ok := payload[name]; !ok && present && (entry.Kind == "runtime" || entry.Kind == "skill") {
			removals = append(removals, name)
		}
	}
	for _, name := range sortedKeys(payload) {
		item := payload[name]
		target := full(root, name)
		if err := safePath(target); err != nil {
			return nil, err
		}
		present, err := exists(target)
		if err != nil {
			return nil, err
		}
		old, owned := oldFiles[name]
		if present && !owned {
			return nil, fmt.Errorf("unmanaged target exists; installation aborted: %s", name)
		}
		var data []byte
		if present {
			data, err = readRegular(target)
			if err != nil {
				return nil, err
			}
		}
		if present && (item.Kind == "config" || item.Kind == "start") {
			records[name] = old
			preserved = append(preserved, name)
		} else {
			records[name] = record{Kind: item.Kind, SHA256: digest(item.Data)}
			if present && bytes.Equal(data, item.Data) {
				unchanged = append(unchanged, name)
			} else {
				writes = append(writes, name)
			}
		}
	}
	requiredDirs := make(map[string]bool)
	requiredDirs[".project-control"] = true
	for _, name := range dataDirs {
		requiredDirs[name] = true
	}
	for name := range payload {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			requiredDirs[parent] = true
		}
	}
	ownedDirs := make(map[string]bool)
	if previous != nil {
		for _, name := range previous.Directories {
			ownedDirs[name] = true
		}
	}
	for name := range requiredDirs {
		target := full(root, name)
		if err := safePath(target); err != nil {
			return nil, err
		}
		info, err := os.Stat(target)
		if errors.Is(err, fs.ErrNotExist) {
			ownedDirs[name] = true
		} else if err != nil {
			return nil, err
		} else if !info.IsDir() {
			return nil, fmt.Errorf("directory target is a file: %s", name)
		}
	}
	for _, name := range append(append([]string{}, writes...), removals...) {
		if name == ".project-control/bin/projectctl" || name == ".project-control/bin/projectctl.exe" {
			if err := windowsRunningBinary(full(root, name)); err != nil {
				return nil, err
			}
		}
	}
	installedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if previous != nil {
		installedAt = previous.InstalledAt
	}
	next := manifest{SchemaVersion: 1, SkillName: skillName, InstallVersion: version, PackageSHA256: digest(packageData), Active: true, Agents: selected, Files: records, Directories: sortedKeys(ownedDirs), InstalledAt: installedAt}
	manifestData, err := jsonBytes(next)
	if err != nil {
		return nil, err
	}
	manifestChanged := true
	if previous != nil {
		current, err := readRegular(full(root, manifestPath))
		if err != nil {
			return nil, err
		}
		manifestChanged = !bytes.Equal(current, manifestData)
	}
	result := map[string]any{
		"ok": true, "operation": "install", "dry_run": opts.DryRun, "project": root,
		"version": version, "agents": selected, "write": writes, "remove": removals,
		"unchanged": unchanged, "preserved": preserved, "manifest_changed": manifestChanged,
	}
	if opts.DryRun {
		return result, nil
	}
	for _, name := range sortedKeys(requiredDirs) {
		if err := mkdir(full(root, name)); err != nil {
			return nil, err
		}
	}
	for _, name := range removals {
		if err := safePath(full(root, name)); err != nil {
			return nil, err
		}
		if err := os.Remove(full(root, name)); err != nil {
			return nil, err
		}
	}
	for _, name := range writes {
		item := payload[name]
		if err := writeAtomic(full(root, name), item.Data, item.Mode); err != nil {
			return nil, err
		}
	}
	if manifestChanged {
		if err := writeAtomic(full(root, manifestPath), manifestData, 0644); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func snapshotTree(root string) (map[string]treeEntry, error) {
	result := make(map[string]treeEntry)
	if err := safePath(root); err != nil {
		return nil, err
	}
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := safePath(name); err != nil {
			return err
		}
		if name == root {
			return nil
		}
		relative, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if !validRelative(relative) {
			return fmt.Errorf("unsafe data path: %s", relative)
		}
		if entry.IsDir() {
			result[relative] = treeEntry{Directory: true}
			return nil
		}
		hash, err := hashRegular(name)
		if err != nil {
			return err
		}
		result[relative] = treeEntry{Hash: hash}
		return nil
	})
	return result, err
}

func sameSnapshot(a, b map[string]treeEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for name, entry := range a {
		if other, ok := b[name]; !ok || other != entry {
			return false
		}
	}
	return true
}

func backupTree(root, backup string, snapshot map[string]treeEntry) error {
	if err := mkdir(filepath.Dir(backup)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(backup), ".project-control-backup-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	defer file.Close()
	archive := zip.NewWriter(file)
	if _, err := archive.Create(".project-control/"); err != nil {
		return err
	}
	for _, name := range sortedKeys(snapshot) {
		entry := snapshot[name]
		archiveName := ".project-control/" + name
		if entry.Directory {
			archiveName += "/"
		}
		writer, err := archive.Create(archiveName)
		if err != nil {
			return err
		}
		if entry.Directory {
			continue
		}
		source := full(root, name)
		if err := safePath(source); err != nil {
			return err
		}
		input, err := os.Open(source)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(writer, hash), input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.Hash {
			return fmt.Errorf("project data changed during backup: %s", name)
		}
	}
	if err := archive.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	check, err := zip.OpenReader(temporary)
	if err != nil {
		return err
	}
	for _, item := range check.File {
		reader, err := item.Open()
		if err != nil {
			check.Close()
			return err
		}
		_, copyErr := io.Copy(io.Discard, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			check.Close()
			return fmt.Errorf("backup integrity check failed: %w", copyErr)
		}
		if closeErr != nil {
			check.Close()
			return closeErr
		}
	}
	if err := check.Close(); err != nil {
		return err
	}
	current, err := snapshotTree(root)
	if err != nil {
		return err
	}
	if !sameSnapshot(snapshot, current) {
		return errors.New("project data changed during backup; no files removed")
	}
	if err := safePath(backup); err != nil {
		return err
	}
	// Same-directory hard-link publication is atomic and cannot overwrite a backup.
	if err := os.Link(temporary, backup); err != nil {
		return fmt.Errorf("cannot publish backup (target must not exist); no files removed: %w", err)
	}
	return nil
}

func within(candidate, root string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

// Uninstall removes only unchanged installed code by default. Its inactive
// manifest receipt retains ownership for safe reinstallation and later purge.
// Purge first verifies a ZIP backup, then removes all .project-control data.
func Uninstall(opts UninstallOptions) (map[string]any, error) {
	root, err := canonicalProject(opts.Project)
	if err != nil {
		return nil, err
	}
	previous, err := readManifest(root)
	if err != nil {
		return nil, err
	}
	if previous == nil {
		return nil, errors.New("no installation manifest found; refusing to guess ownership")
	}
	if opts.Purge && opts.Backup == "" {
		return nil, errors.New("--purge requires --backup PATH")
	}
	if !opts.Purge && opts.Backup != "" {
		return nil, errors.New("--backup is only valid with --purge")
	}
	removals, modified, preserved := []string{}, []string{}, []string{}
	for _, name := range sortedKeys(previous.Files) {
		entry := previous.Files[name]
		present, err := exists(full(root, name))
		if err != nil {
			return nil, err
		}
		if !present {
			continue
		}
		data, err := readRegular(full(root, name))
		if err != nil {
			return nil, err
		}
		if entry.Kind == "config" || entry.Kind == "start" {
			preserved = append(preserved, name)
		} else if digest(data) == entry.SHA256 {
			removals = append(removals, name)
		} else {
			modified = append(modified, name)
		}
	}
	pc := full(root, ".project-control")
	var snapshot map[string]treeEntry
	backup := ""
	if opts.Purge {
		backup, err = filepath.Abs(opts.Backup)
		if err != nil {
			return nil, err
		}
		if err := safePath(backup); err != nil {
			return nil, err
		}
		if within(backup, pc) {
			return nil, errors.New("backup must be outside .project-control")
		}
		for _, prefix := range agentRoots {
			if within(backup, full(root, prefix)) {
				return nil, errors.New("backup must be outside managed skill directories")
			}
		}
		if present, err := exists(backup); err != nil {
			return nil, err
		} else if present {
			return nil, errors.New("backup target already exists; refusing to overwrite")
		}
		snapshot, err = snapshotTree(pc)
		if err != nil {
			return nil, err
		}
		filtered := []string{}
		for _, name := range removals {
			if !strings.HasPrefix(name, ".project-control/") {
				filtered = append(filtered, name)
			}
		}
		removals = filtered
		for name, entry := range snapshot {
			if !entry.Directory {
				removals = append(removals, ".project-control/"+name)
			}
		}
		filtered = []string{}
		for _, name := range modified {
			if !strings.HasPrefix(name, ".project-control/") {
				filtered = append(filtered, name)
			}
		}
		modified, preserved = filtered, []string{}
	} else {
		preserved = append(preserved, manifestPath)
	}
	removals = unique(removals)
	for _, name := range removals {
		if name == ".project-control/bin/projectctl" || name == ".project-control/bin/projectctl.exe" {
			if err := windowsRunningBinary(full(root, name)); err != nil {
				return nil, err
			}
		}
	}
	result := map[string]any{
		"ok": true, "operation": "uninstall", "project": root, "dry_run": opts.DryRun,
		"purge": opts.Purge, "remove": removals, "preserved_modified": modified,
		"preserved": preserved, "project_data_preserved": !opts.Purge, "backup": backup,
		"receipt_preserved": !opts.Purge,
	}
	if opts.DryRun {
		return result, nil
	}
	if opts.Purge {
		if err := backupTree(pc, backup, snapshot); err != nil {
			return nil, err
		}
		hash, err := hashRegular(backup)
		if err != nil {
			return nil, err
		}
		result["backup_sha256"] = hash
	}
	for _, name := range removals {
		target := full(root, name)
		hash, err := hashRegular(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		expected := previous.Files[name].SHA256
		if opts.Purge && strings.HasPrefix(name, ".project-control/") {
			expected = snapshot[strings.TrimPrefix(name, ".project-control/")].Hash
		}
		if hash != expected {
			return nil, fmt.Errorf("file changed during uninstall; refusing deletion: %s", name)
		}
		if err := safePath(target); err != nil {
			return nil, err
		}
		if err := os.Remove(target); err != nil {
			return nil, fmt.Errorf("cannot remove %s (stop any process using this file; on Windows run an external distribution binary): %w", name, err)
		}
	}
	if !opts.Purge {
		// Retain original hashes even for locally modified files: a later install
		// must still refuse to overwrite them instead of silently adopting them.
		previous.Active = false
		data, err := jsonBytes(previous)
		if err != nil {
			return nil, err
		}
		if err := writeAtomic(full(root, manifestPath), data, 0644); err != nil {
			return nil, err
		}
	}
	directories := append([]string{}, previous.Directories...)
	if opts.Purge {
		for name, entry := range snapshot {
			if entry.Directory {
				directories = append(directories, ".project-control/"+name)
			}
		}
		directories = append(directories, ".project-control")
	}
	directories = unique(directories)
	sort.Slice(directories, func(i, j int) bool {
		if strings.Count(directories[i], "/") != strings.Count(directories[j], "/") {
			return strings.Count(directories[i], "/") > strings.Count(directories[j], "/")
		}
		return directories[i] > directories[j]
	})
	for _, name := range directories {
		target := full(root, name)
		if err := safePath(target); err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(target)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			if err := os.Remove(target); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}
