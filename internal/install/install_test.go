package install

import (
	"archive/zip"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
)

type fixture struct {
	t       *testing.T
	base    string
	project string
	binary  string
	assets  fstest.MapFS
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, base: base, project: filepath.Join(base, "project"), binary: filepath.Join(base, "distribution"), assets: fstest.MapFS{
		"SKILL.md":               &fstest.MapFile{Data: []byte("---\nname: github-project-control\n---\nFixture skill\n")},
		"references/protocol.md": &fstest.MapFile{Data: []byte("# Protocol\n")},
	}}
	f.write(f.binary, "fixture binary v1")
	return f
}

func (f *fixture) write(name, content string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) read(name string) string {
	f.t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) opts() Options {
	return Options{Project: f.project, Assets: f.assets, Executable: f.binary, Version: "1.0.0"}
}

func (f *fixture) install() map[string]any {
	f.t.Helper()
	result, err := Install(f.opts())
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

func (f *fixture) manifest() *manifest {
	f.t.Helper()
	value, err := readManifest(f.project)
	if err != nil {
		f.t.Fatal(err)
	}
	if value == nil {
		f.t.Fatal("missing manifest")
	}
	return value
}

func (f *fixture) saveManifest(value *manifest) {
	f.t.Helper()
	data, err := jsonBytes(value)
	if err != nil {
		f.t.Fatal(err)
	}
	f.write(full(f.project, manifestPath), string(data))
}

func (f *fixture) tree() map[string]string {
	f.t.Helper()
	result := map[string]string{}
	if _, err := os.Stat(f.project); os.IsNotExist(err) {
		return result
	}
	err := filepath.WalkDir(f.project, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(f.project, name)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			result[relative] = "<directory>"
			return nil
		}
		data, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		result[relative] = string(data)
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return result
}

func binaryRelative() string {
	if runtime.GOOS == "windows" {
		return ".project-control/bin/projectctl.exe"
	}
	return ".project-control/bin/projectctl"
}

func mustExist(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Stat(name); err != nil {
		t.Fatal(err)
	}
}
func mustAbsent(t *testing.T, name string) {
	t.Helper()
	if _, err := os.Lstat(name); !os.IsNotExist(err) {
		t.Fatalf("expected absent %s, got %v", name, err)
	}
}
func mustError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected error containing %q, got %v", contains, err)
	}
}

func TestInstallEmptyProject(t *testing.T) {
	f := newFixture(t)
	result := f.install()
	if result["ok"] != true {
		t.Fatal(result)
	}
	mustExist(t, full(f.project, ".agents/skills/github-project-control/SKILL.md"))
	mustExist(t, full(f.project, binaryRelative()))
	var config map[string]any
	if err := json.Unmarshal([]byte(f.read(full(f.project, ".project-control/config.json"))), &config); err != nil {
		t.Fatal(err)
	}
	if config["project_name"] != "project" || config["max_attempts"] != float64(3) || config["context_max_chars"] != float64(60000) {
		t.Fatal(config)
	}
	if config["autonomy"].(map[string]any)["remote_writes"] != false {
		t.Fatal(config)
	}
	ignore := f.read(full(f.project, ".project-control/.gitignore"))
	for _, expected := range []string{"bin/", "runtime.lock", "state.json"} {
		if !strings.Contains(ignore, expected) {
			t.Fatal(ignore)
		}
	}
	if !f.manifest().Active {
		t.Fatal("new installation not active")
	}
	mustAbsent(t, filepath.Join(f.project, ".git"))
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(full(f.project, binaryRelative()))
		if info.Mode().Perm()&0111 == 0 {
			t.Fatal("binary not executable")
		}
	}
}

func TestExistingProjectPreserved(t *testing.T) {
	f := newFixture(t)
	originals := map[string]string{"AGENTS.md": "user instructions", "CLAUDE.md": "other instructions", ".gitignore": "user ignore", "main.go": "package main", ".github/workflows/user.yml": "user workflow"}
	for name, content := range originals {
		f.write(full(f.project, name), content)
	}
	opts := f.opts()
	opts.Agents = []string{"codex", "claude"}
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	for name, content := range originals {
		if f.read(full(f.project, name)) != content {
			t.Fatalf("changed user file %s", name)
		}
	}
	mustExist(t, full(f.project, ".claude/skills/github-project-control/SKILL.md"))
}

func TestGenericInstallsSkillAndRuntime(t *testing.T) {
	f := newFixture(t)
	opts := f.opts()
	opts.Agents = []string{"generic"}
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	mustExist(t, full(f.project, ".project-control/skill/SKILL.md"))
	mustExist(t, full(f.project, binaryRelative()))
	mustAbsent(t, full(f.project, ".agents"))
	mustAbsent(t, full(f.project, ".claude"))
}

func TestDryRunNoWrites(t *testing.T) {
	f := newFixture(t)
	opts := f.opts()
	opts.DryRun = true
	result, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(result["write"].([]string)) == 0 {
		t.Fatal("missing plan")
	}
	mustAbsent(t, f.project)
	f.install()
	before := f.tree()
	if _, err := Uninstall(UninstallOptions{Project: f.project, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, f.tree()) {
		t.Fatal("dry-run mutated files")
	}
}

func TestInstallIdempotent(t *testing.T) {
	f := newFixture(t)
	f.install()
	before := f.tree()
	result := f.install()
	if len(result["write"].([]string)) != 0 || result["manifest_changed"] != false {
		t.Fatal(result)
	}
	if !reflect.DeepEqual(before, f.tree()) {
		t.Fatal("identical installation changed files")
	}
}

func TestUpgradePreservesConfigStartAndData(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.write(full(f.project, ".project-control/config.json"), `{"project_name":"custom"}`)
	f.write(full(f.project, ".project-control/START_HERE.md"), "user start")
	f.write(full(f.project, ".project-control/tasks/TASK-001.json"), "project data")
	f.write(f.binary, "fixture binary v2")
	_, err := Install(f.opts())
	mustError(t, err, "--upgrade")
	opts := f.opts()
	opts.Upgrade = true
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	if f.read(full(f.project, binaryRelative())) != "fixture binary v2" {
		t.Fatal("binary not upgraded")
	}
	for name, content := range map[string]string{"config.json": `{"project_name":"custom"}`, "START_HERE.md": "user start", "tasks/TASK-001.json": "project data"} {
		if f.read(full(f.project, ".project-control/"+name)) != content {
			t.Fatal("changed " + name)
		}
	}
}

func TestUpgradeConflictPreflight(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.write(full(f.project, ".agents/skills/github-project-control/references/protocol.md"), "user changed it")
	f.write(f.binary, "fixture binary v2")
	before := f.tree()
	opts := f.opts()
	opts.Upgrade = true
	_, err := Install(opts)
	mustError(t, err, "locally modified")
	if !reflect.DeepEqual(before, f.tree()) {
		t.Fatal("upgrade conflict caused mutations")
	}
}

func TestUpgradeRemovesObsoleteAndPreservesUnknownFiles(t *testing.T) {
	f := newFixture(t)
	f.install()
	delete(f.assets, "references/protocol.md")
	f.write(full(f.project, ".agents/skills/github-project-control/user.txt"), "user notes")
	opts := f.opts()
	opts.Upgrade = true
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	mustAbsent(t, full(f.project, ".agents/skills/github-project-control/references/protocol.md"))
	if f.read(full(f.project, ".agents/skills/github-project-control/user.txt")) != "user notes" {
		t.Fatal("removed user notes")
	}
}

func TestUpgradeAddsAgents(t *testing.T) {
	f := newFixture(t)
	f.install()
	opts := f.opts()
	opts.Upgrade = true
	opts.Agents = []string{"claude", "generic"}
	if _, err := Install(opts); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.manifest().Agents, []string{"claude", "codex", "generic"}) {
		t.Fatal("lost integration")
	}
	for _, prefix := range agentRoots {
		mustExist(t, full(f.project, prefix+"/SKILL.md"))
	}
}

func TestUnmanagedTargetsRejected(t *testing.T) {
	for _, target := range []string{".project-control/notes.txt", ".agents/skills/github-project-control/SKILL.md"} {
		t.Run(target, func(t *testing.T) {
			f := newFixture(t)
			f.write(full(f.project, target), "user file")
			before := f.tree()
			_, err := Install(f.opts())
			mustError(t, err, "unmanaged")
			if !reflect.DeepEqual(before, f.tree()) {
				t.Fatal("adopted unmanaged files")
			}
		})
	}
}

func TestNewAssetCollisionPreflight(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.assets["new.md"] = &fstest.MapFile{Data: []byte("new source")}
	f.write(full(f.project, ".agents/skills/github-project-control/new.md"), "user file")
	before := f.tree()
	opts := f.opts()
	opts.Upgrade = true
	_, err := Install(opts)
	mustError(t, err, "unmanaged target")
	if !reflect.DeepEqual(before, f.tree()) {
		t.Fatal("collision mutated files")
	}
}

func TestDefaultUninstallPreservesDataModifiedAndReceipt(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.write(full(f.project, binaryRelative()), "user modified binary")
	f.write(full(f.project, ".project-control/tasks/TASK-001.json"), "project data")
	f.write(full(f.project, "main.go"), "user code")
	result, err := Uninstall(UninstallOptions{Project: f.project})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result["preserved_modified"], []string{binaryRelative()}) {
		t.Fatal(result)
	}
	if f.read(full(f.project, binaryRelative())) != "user modified binary" || f.read(full(f.project, ".project-control/tasks/TASK-001.json")) != "project data" || f.read(full(f.project, "main.go")) != "user code" {
		t.Fatal("lost user data")
	}
	mustExist(t, full(f.project, ".project-control/config.json"))
	mustExist(t, full(f.project, ".project-control/START_HERE.md"))
	if f.manifest().Active {
		t.Fatal("receipt still active")
	}
	mustAbsent(t, full(f.project, ".agents/skills/github-project-control/SKILL.md"))
	_, err = Install(f.opts())
	mustError(t, err, "locally modified")
}

func TestUninstallReinstallRetainsMemory(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.write(full(f.project, ".project-control/config.json"), `{"project_name":"custom"}`)
	f.write(full(f.project, ".project-control/tasks/TASK-001.json"), "persistent task")
	if _, err := Uninstall(UninstallOptions{Project: f.project}); err != nil {
		t.Fatal(err)
	}
	if f.manifest().Active {
		t.Fatal("receipt still active")
	}
	// Repeated removal remains safe, then the same package can restore code.
	if _, err := Uninstall(UninstallOptions{Project: f.project}); err != nil {
		t.Fatal(err)
	}
	f.install()
	if !f.manifest().Active {
		t.Fatal("reinstallation not active")
	}
	if f.read(full(f.project, ".project-control/config.json")) != `{"project_name":"custom"}` || f.read(full(f.project, ".project-control/tasks/TASK-001.json")) != "persistent task" {
		t.Fatal("reinstall lost memory")
	}
	mustExist(t, full(f.project, binaryRelative()))
}

func TestUninstallPreservesUnknownSkillAndParentFiles(t *testing.T) {
	f := newFixture(t)
	f.write(full(f.project, ".agents/skills/other.txt"), "other")
	f.install()
	f.write(full(f.project, ".agents/skills/github-project-control/user.txt"), "keep")
	if _, err := Uninstall(UninstallOptions{Project: f.project}); err != nil {
		t.Fatal(err)
	}
	if f.read(full(f.project, ".agents/skills/other.txt")) != "other" || f.read(full(f.project, ".agents/skills/github-project-control/user.txt")) != "keep" {
		t.Fatal("removed unowned files")
	}
}

func TestPurgeRequiresBackup(t *testing.T) {
	f := newFixture(t)
	f.install()
	before := f.tree()
	_, err := Uninstall(UninstallOptions{Project: f.project, Purge: true})
	mustError(t, err, "requires --backup")
	if !reflect.DeepEqual(before, f.tree()) {
		t.Fatal("purge without backup mutated files")
	}
}

func TestPurgeBacksUpEntireMemory(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.write(full(f.project, ".project-control/tasks/nested/data.json"), "new data")
	if err := os.Mkdir(full(f.project, ".project-control/new-empty-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	f.write(full(f.project, ".agents/skills/github-project-control/SKILL.md"), "modified skill")
	f.write(full(f.project, "keep.txt"), "user file")
	backup := filepath.Join(f.base, "backup", "project.zip")
	if _, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: backup, DryRun: true}); err != nil {
		t.Fatal(err)
	}
	mustAbsent(t, filepath.Dir(backup))
	result, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: backup})
	if err != nil {
		t.Fatal(err)
	}
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if result["backup_sha256"] != digest(backupData) {
		t.Fatal("wrong backup hash")
	}
	mustAbsent(t, full(f.project, ".project-control"))
	if f.read(full(f.project, ".agents/skills/github-project-control/SKILL.md")) != "modified skill" || f.read(full(f.project, "keep.txt")) != "user file" {
		t.Fatal("lost files outside project memory")
	}
	archive, err := zip.OpenReader(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	entries := map[string]string{}
	for _, entry := range archive.File {
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[entry.Name] = string(data)
	}
	if entries[".project-control/tasks/nested/data.json"] != "new data" {
		t.Fatal("missing task data")
	}
	for _, name := range []string{".project-control/new-empty-dir/", ".project-control/manifest.json"} {
		if _, ok := entries[name]; !ok {
			t.Fatal("missing " + name)
		}
	}
}

func TestPurgeAfterDefaultUninstall(t *testing.T) {
	f := newFixture(t)
	f.install()
	f.write(full(f.project, ".project-control/tasks/retained.json"), "retained")
	if _, err := Uninstall(UninstallOptions{Project: f.project}); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(f.base, "after-uninstall.zip")
	if _, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: backup}); err != nil {
		t.Fatal(err)
	}
	mustAbsent(t, full(f.project, ".project-control"))
	mustExist(t, backup)
}

func TestUnsafeBackupDestinations(t *testing.T) {
	f := newFixture(t)
	f.install()
	for _, backup := range []string{full(f.project, ".project-control/backup.zip"), full(f.project, ".agents/skills/github-project-control/backup.zip")} {
		_, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: backup})
		mustError(t, err, "outside")
	}
	backup := filepath.Join(f.base, "backup.zip")
	f.write(backup, "old backup")
	_, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: backup})
	mustError(t, err, "already exists")
	if f.read(backup) != "old backup" {
		t.Fatal("overwrote backup")
	}
	mustExist(t, full(f.project, manifestPath))
}

func TestMaliciousManifestPathsDoNotDeleteOutside(t *testing.T) {
	for _, attack := range []string{"../outside.txt", "/outside.txt", "main.go", ".project-control/bin/../outside.txt", ".project-control/bin/projectctl:stream", ".agents/skills/github-project-control/../../victim"} {
		t.Run(attack, func(t *testing.T) {
			f := newFixture(t)
			f.install()
			outside := filepath.Join(f.base, "outside.txt")
			f.write(outside, "precious")
			value := f.manifest()
			value.Files[attack] = record{Kind: "runtime", SHA256: digest([]byte("precious"))}
			f.saveManifest(value)
			_, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: filepath.Join(f.base, "bad.zip")})
			mustError(t, err, "manifest")
			if f.read(outside) != "precious" {
				t.Fatal("outside deletion")
			}
			mustExist(t, full(f.project, binaryRelative()))
			mustAbsent(t, filepath.Join(f.base, "bad.zip"))
		})
	}
}

func TestMaliciousManifestDirectoryRejected(t *testing.T) {
	f := newFixture(t)
	f.install()
	if err := os.Mkdir(full(f.project, "user-empty-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	value := f.manifest()
	value.Directories = append(value.Directories, "user-empty-dir")
	f.saveManifest(value)
	_, err := Uninstall(UninstallOptions{Project: f.project})
	mustError(t, err, "unmanaged manifest directory")
	mustExist(t, full(f.project, "user-empty-dir"))
}

func symlinkOrSkip(t *testing.T, source, target string) {
	t.Helper()
	if err := os.Symlink(source, target); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("symlink privilege unavailable: " + err.Error())
		}
		t.Fatal(err)
	}
}

func TestExplicitProjectSymlinkCanonicalized(t *testing.T) {
	f := newFixture(t)
	actual := filepath.Join(f.base, "actual")
	if err := os.Mkdir(actual, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.base, "linked")
	symlinkOrSkip(t, actual, link)
	opts := f.opts()
	opts.Project = filepath.Join(link, "new-project")
	result, err := Install(opts)
	if err != nil {
		t.Fatal(err)
	}
	if result["project"] != filepath.Join(actual, "new-project") {
		t.Fatal(result)
	}
	mustExist(t, filepath.Join(actual, "new-project", ".project-control", "manifest.json"))
}

func TestSymlinkDestinationRejectedBeforeWrites(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(f.base, "outside")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.project, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, full(f.project, ".agents"))
	_, err := Install(f.opts())
	mustError(t, err, "symlink")
	mustAbsent(t, full(f.project, ".project-control"))
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside project")
	}
}

func TestSymlinkManagedFileNeverDeletesExternal(t *testing.T) {
	f := newFixture(t)
	f.install()
	outside := filepath.Join(f.base, "outside")
	f.write(outside, "precious")
	target := full(f.project, binaryRelative())
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, target)
	_, err := Uninstall(UninstallOptions{Project: f.project})
	mustError(t, err, "symlink")
	if f.read(outside) != "precious" {
		t.Fatal("outside file deleted")
	}
	mustExist(t, full(f.project, manifestPath))
}

func TestSymlinkUntrackedDataBlocksPurge(t *testing.T) {
	f := newFixture(t)
	f.install()
	outside := filepath.Join(f.base, "outside")
	f.write(filepath.Join(outside, "precious.txt"), "precious")
	symlinkOrSkip(t, outside, full(f.project, ".project-control/tasks/link"))
	backup := filepath.Join(f.base, "backup.zip")
	_, err := Uninstall(UninstallOptions{Project: f.project, Purge: true, Backup: backup})
	mustError(t, err, "symlink")
	if f.read(filepath.Join(outside, "precious.txt")) != "precious" {
		t.Fatal("outside data deleted")
	}
	mustAbsent(t, backup)
	mustExist(t, full(f.project, manifestPath))
}

func TestSymlinkAssetRejected(t *testing.T) {
	f := newFixture(t)
	f.assets["link.md"] = &fstest.MapFile{Data: []byte("target"), Mode: fs.ModeSymlink}
	_, err := Install(f.opts())
	mustError(t, err, "symlink asset")
	mustAbsent(t, f.project)
}

func TestInvalidAssetPathAndMissingSkill(t *testing.T) {
	f := newFixture(t)
	delete(f.assets, "SKILL.md")
	_, err := Install(f.opts())
	mustError(t, err, "SKILL.md")
	if validRelative(".agents/skills/foo/CON.txt") || validRelative("a/../b") || validRelative("a:b") || validRelative("a\\b") {
		t.Fatal("accepted unsafe relative path")
	}
}

func TestWindowsRunningBinaryGuard(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only running executable check")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	mustError(t, windowsRunningBinary(self), "external distribution binary")
}
