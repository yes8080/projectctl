// Command release builds the standalone projectctl distribution using only Go.
// Cross-compilation checks that targets build; it does not execute those targets.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var safeVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

type target struct{ goos, goarch string }

type archiveAsset struct {
	name string
	data []byte
}

var targets = []target{
	{"darwin", "amd64"}, {"darwin", "arm64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

func main() {
	version := flag.String("version", "", "release version, e.g. v1.0.0 or v1.0.0-rc.1")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "release: unexpected positional arguments")
		os.Exit(1)
	}
	if err := buildRelease(*version); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func buildRelease(version string) error {
	if len(version) > 80 || !safeVersion.MatchString(version) {
		return fmt.Errorf("--version must be a safe vMAJOR.MINOR.PATCH version with an optional prerelease suffix")
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := regularFile(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("run from the repository root: %w", err)
	}
	assets, err := releaseAssets(root)
	if err != nil {
		return err
	}
	dist := filepath.Join(root, "dist")
	if info, err := os.Lstat(dist); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("dist must be a real directory")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dist, 0755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(dist, ".release-build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	names := make([]string, 0, len(targets))
	var sums strings.Builder
	for _, platform := range targets {
		name := fmt.Sprintf("projectctl_%s_%s_%s.zip", version, platform.goos, platform.goarch)
		binaryName := "projectctl"
		if platform.goos == "windows" {
			binaryName += ".exe"
		}
		binaryPath := filepath.Join(stage, platform.goos+"-"+platform.goarch+"-"+binaryName)
		fmt.Printf("Building %s/%s\n", platform.goos, platform.goarch)
		command := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w -X main.version="+version,
			"-o", binaryPath, "./cmd/projectctl")
		command.Dir = root
		command.Env = buildEnv(platform)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("build %s/%s failed: %w", platform.goos, platform.goarch, err)
		}
		archivePath := filepath.Join(stage, name)
		if err := writeArchive(archivePath, binaryPath, binaryName, assets); err != nil {
			return err
		}
		checksum, err := fileChecksum(archivePath)
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", checksum, name)
		names = append(names, name)
	}
	// The manifest only exists once all six builds and ZIP files are complete.
	if err := os.WriteFile(filepath.Join(stage, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	for _, name := range append(append([]string{}, names...), "SHA256SUMS") {
		info, err := os.Lstat(filepath.Join(dist, name))
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace non-regular output: %s", name)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Do not leave an old manifest describing partly replaced output if publication
	// fails. Previous releases remain unchanged if any earlier build step failed.
	manifest := filepath.Join(dist, "SHA256SUMS")
	if err := os.Remove(manifest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, name := range names {
		if err := os.Rename(filepath.Join(stage, name), filepath.Join(dist, name)); err != nil {
			return fmt.Errorf("publish %s (no new SHA256SUMS written): %w", name, err)
		}
	}
	if err := os.Rename(filepath.Join(stage, "SHA256SUMS"), manifest); err != nil {
		return fmt.Errorf("publish SHA256SUMS: %w", err)
	}
	fmt.Printf("Created %d release ZIPs and dist/SHA256SUMS. Cross-compiled binaries have not been run on their target platforms.\n", len(names))
	return nil
}

func buildEnv(platform target) []string {
	// Do not inherit user GOFLAGS, workspace overrides, or an automatic toolchain
	// download. All dependencies are in this repository or the local standard library.
	overrides := map[string]string{
		"GOOS": platform.goos, "GOARCH": platform.goarch, "CGO_ENABLED": "0",
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off",
		"GOWORK": "off", "GOFLAGS": "",
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key := strings.SplitN(item, "=", 2)[0]
		if _, replace := overrides[strings.ToUpper(key)]; !replace {
			env = append(env, item)
		}
	}
	for _, key := range []string{"GOOS", "GOARCH", "CGO_ENABLED", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOWORK", "GOFLAGS"} {
		env = append(env, key+"="+overrides[key])
	}
	return env
}

func regularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file: %s", path)
	}
	return os.ReadFile(path)
}

// releaseAssets includes only documentation, portable skill resources, and the
// small runnable example. It never walks the repository root, runtime ledgers,
// installed Agent directories, or dist. All files are snapshotted before builds.
func releaseAssets(root string) ([]archiveAsset, error) {
	names := []string{"README.md", "README.en.md", "LICENSE", "skills/github-project-control/SKILL.md"}
	for _, directory := range []string{
		"docs", "skills/github-project-control/agents",
		"skills/github-project-control/references", "examples/hello-go",
	} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(directory)), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if strings.HasPrefix(entry.Name(), ".") {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("release resources cannot be symlinks: %s", path)
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("release resources must be ordinary files: %s", path)
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			names = append(names, filepath.ToSlash(relative))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(names)
	assets := make([]archiveAsset, 0, len(names))
	for _, name := range names {
		data, err := regularFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		assets = append(assets, archiveAsset{name: name, data: data})
	}
	return assets, nil
}

func writeArchive(path, binaryPath, binaryName string, assets []archiveAsset) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()
	archive := zip.NewWriter(file)
	write := func(name string, mode os.FileMode, content io.Reader) error {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(mode)
		header.SetModTime(time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC))
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}
		_, err = io.Copy(entry, content)
		return err
	}
	binary, err := os.Open(binaryPath)
	if err != nil {
		archive.Close()
		return err
	}
	err = write(binaryName, 0755, binary)
	closeErr := binary.Close()
	if err == nil {
		err = closeErr
	}
	for _, asset := range assets {
		if err != nil {
			break
		}
		err = write(asset.name, 0644, bytes.NewReader(asset.data))
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	return err
}

func fileChecksum(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}
