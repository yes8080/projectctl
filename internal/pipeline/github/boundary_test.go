package github

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestGatewayHasNoLocalLedgerOrCredentialDiscovery(t *testing.T) {
	forbidden := map[string]bool{"os": true, "os/exec": true, "io/ioutil": true, "database/sql": true, "syscall": true, "github.com/yes8080/projectctl/internal/github": true, "github.com/yes8080/projectctl/internal/control": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range file.Imports {
			name, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if forbidden[name] {
				t.Errorf("%s crosses Gateway boundary with %s", entry.Name(), name)
			}
		}
	}
}

func TestNativeJSONCannotSilentlyReplaceInvalidUnicode(t *testing.T) {
	for _, body := range []string{`{"body":"\ud800"}`, `{"body":"\udc00"}`, `{"body":"\ud800\u1234"}`} {
		var native struct {
			Body string `json:"body"`
		}
		if err := decodeAPI([]byte(body), &native); err == nil {
			t.Fatal("lone surrogate silently replaced in native content")
		}
	}
	var native struct {
		Body string `json:"body"`
	}
	if err := decodeAPI([]byte(`{"body":"\ud83d\ude00"}`), &native); err != nil || native.Body != "😀" {
		t.Fatalf("valid surrogate pair rejected: %q %v", native.Body, err)
	}
}

func TestCredentialSnapshotsDoNotCrossGatewayInstances(t *testing.T) {
	f := newWriteFixture(t)
	first := f.gateway(t, nil)
	second := f.gateway(t, nil)
	first.token = func(context.Context) (string, error) { return "first-secret", nil }
	secondCalls := 0
	second.token = func(context.Context) (string, error) { secondCalls++; return "synthetic-test-bearer", nil }
	bound, err := first.bindCredential(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.CurrentUser(bound); err != nil {
		t.Fatal(err)
	}
	if secondCalls != 1 {
		t.Fatal("another Gateway reused a caller's privileged credential snapshot")
	}
}
