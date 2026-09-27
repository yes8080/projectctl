package startup

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestProductionCoreHasNoLocalOrRuntimeSideChannel(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Clean(name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == "os" || path == "os/exec" || path == "net/http" || path == "syscall" || strings.HasPrefix(path, "database/") || strings.Contains(path, "/internal/control") || strings.Contains(path, "/internal/team") || strings.Contains(path, "/internal/github") {
				t.Errorf("startup core imports local persistence/runtime/legacy/HTTP side channel: %s in %s", path, name)
			}
		}
	}
}
