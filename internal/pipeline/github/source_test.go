package github

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type sourceTestFixture struct {
	ref     protocol.SourceReference
	data    []byte
	blobSHA string
	commit  map[string]any
	root    map[string]any
	dir     map[string]any
	blob    map[string]any
	objects map[string]map[string]any
	calls   atomic.Int64
}

const sourceTestPrefix = "/repos/octo/pipeline/git/"

func sourceTestURL(kind, sha string) string {
	return "https://api.github.com" + sourceTestPrefix + kind + "/" + sha
}

func newSourceTestFixture(data []byte) *sourceTestFixture {
	commitSHA, rootSHA, dirSHA := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	blobSHA := fmt.Sprintf("%x", sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...)))
	f := &sourceTestFixture{ref: protocol.SourceReference{Commit: commitSHA, Path: "docs/input.md", SHA256: protocol.SHA256(data)}, data: data, blobSHA: blobSHA}
	f.commit = map[string]any{"sha": commitSHA, "url": sourceTestURL("commits", commitSHA), "tree": map[string]any{"sha": rootSHA, "url": sourceTestURL("trees", rootSHA)}}
	f.root = map[string]any{"sha": rootSHA, "url": sourceTestURL("trees", rootSHA), "truncated": false, "tree": []any{
		map[string]any{"path": "docs", "mode": "040000", "type": "tree", "sha": dirSHA, "url": sourceTestURL("trees", dirSHA)},
	}}
	f.dir = map[string]any{"sha": dirSHA, "url": sourceTestURL("trees", dirSHA), "truncated": false, "tree": []any{
		map[string]any{"path": "input.md", "mode": "100644", "type": "blob", "sha": blobSHA, "size": len(data), "url": sourceTestURL("blobs", blobSHA)},
	}}
	f.blob = map[string]any{"sha": blobSHA, "url": sourceTestURL("blobs", blobSHA), "encoding": "base64", "content": base64.StdEncoding.EncodeToString(data), "size": len(data)}
	f.objects = map[string]map[string]any{
		sourceTestPrefix + "commits/" + commitSHA: f.commit,
		sourceTestPrefix + "trees/" + rootSHA:     f.root,
		sourceTestPrefix + "trees/" + dirSHA:      f.dir,
		sourceTestPrefix + "blobs/" + blobSHA:     f.blob,
	}
	return f
}

func (f *sourceTestFixture) entry() map[string]any {
	return f.dir["tree"].([]any)[0].(map[string]any)
}

func (f *sourceTestFixture) directory() map[string]any {
	return f.root["tree"].([]any)[0].(map[string]any)
}

func (f *sourceTestFixture) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("Authorization") != "Bearer synthetic-test-bearer" {
			t.Errorf("source request lost read-only, nonrecursive JSON or credential boundary: %s %s", r.Method, r.URL)
		}
		object, ok := f.objects[r.URL.Path]
		if !ok {
			t.Errorf("unexpected source lookup: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		testServerFixtureJSON(t, w, object)
	}
}

func TestReadSourcePinnedGitBytes(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		mode string
	}{
		{"regular", []byte("hello\n"), "100644"},
		{"empty", []byte{}, "100644"},
		{"executable binary", []byte{0, 0xff, 0xfe, '\r', '\n'}, "100755"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newSourceTestFixture(test.data)
			f.entry()["mode"] = test.mode
			encoded := f.blob["content"].(string)
			f.blob["content"] = encoded[:len(encoded)/2] + "\r\n" + encoded[len(encoded)/2:]
			g, server := testServerFixtureGateway(t, f.handler(t))
			for read := 0; read < 2; read++ {
				if read == 1 {
					var err error
					g, err = New(Config{Project: testServerFixtureProject(), BaseURL: server.URL, AllowLoopbackTestServer: true, HTTPClient: server.Client(), Token: func(context.Context) (string, error) { return "synthetic-test-bearer", nil }})
					if err != nil {
						t.Fatal(err)
					}
				}
				got, err := g.ReadSource(context.Background(), f.ref)
				if err != nil || got.Reference != f.ref || got.BlobSHA != f.blobSHA || !bytes.Equal(got.Bytes, f.data) {
					t.Fatalf("exact source failed: got=%+v err=%v", got, err)
				}
				if test.name == "regular" && got.BlobSHA != "ce013625030ba8dba906f756967f9e9ca394464a" {
					t.Fatal("Git blob framing disagrees with known hash")
				}
				if len(got.Bytes) > 0 {
					got.Bytes[0] ^= 0xff
				}
			}
			if f.calls.Load() != 8 {
				t.Fatalf("restart did not reread complete remote chain: %d requests", f.calls.Load())
			}
		})
	}
}

func TestReadSourceRootPathAndUnrelatedNativeObjects(t *testing.T) {
	f := newSourceTestFixture([]byte("hello\n"))
	f.ref.Path = "input.md"
	f.root["tree"] = []any{f.entry(),
		map[string]any{"path": "link", "mode": "120000", "type": "blob", "sha": strings.Repeat("d", 40)},
		map[string]any{"path": "submodule", "mode": "160000", "type": "commit", "sha": strings.Repeat("e", 40)},
	}
	f.root["url"] = "https://api.github.com/repos/octo/pipeline/trees/" + strings.Repeat("b", 40)
	g, _ := testServerFixtureGateway(t, f.handler(t))
	if _, err := g.ReadSource(context.Background(), f.ref); err != nil || f.calls.Load() != 3 {
		t.Fatalf("regular root file rejected because unrelated objects exist: %v", err)
	}
}

func TestReadSourceRejectsMutableOrUnsafeReferencesBeforeHTTP(t *testing.T) {
	valid := newSourceTestFixture([]byte("hello\n")).ref
	for _, value := range []string{"main", "refs/heads/main", "v1.0", "a123456", strings.Repeat("A", 40), strings.Repeat("g", 40), strings.Repeat("a", 39)} {
		t.Run("commit "+value, func(t *testing.T) {
			ref := valid
			ref.Commit = value
			assertSourceInvalidBeforeHTTP(t, ref)
		})
	}
	for _, value := range []string{"", "/docs/input.md", "../input.md", "docs/../input.md", "docs/./input.md", "docs//input.md", "docs/", ".", "..", "docs\\input.md", "docs/%2finput.md", "docs/%252finput.md", "docs/%2e%2e/input.md", "C:/docs/input.md", "docs/\x00input.md", "docs/\ninput.md", string([]byte{0xff}), strings.Repeat("a", maxSourcePathBytes+1), strings.Repeat("a/", maxSourceDepth) + "input.md"} {
		t.Run(fmt.Sprintf("path %q", value), func(t *testing.T) {
			ref := valid
			ref.Path = value
			assertSourceInvalidBeforeHTTP(t, ref)
		})
	}
	for _, value := range []string{"", strings.Repeat("A", 64), strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		t.Run("digest "+value, func(t *testing.T) {
			ref := valid
			ref.SHA256 = value
			assertSourceInvalidBeforeHTTP(t, ref)
		})
	}
}

func assertSourceInvalidBeforeHTTP(t *testing.T, ref protocol.SourceReference) {
	t.Helper()
	var calls atomic.Int64
	g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	got, err := g.ReadSource(context.Background(), ref)
	if err == nil || got.Bytes != nil || got.BlobSHA != "" || calls.Load() != 0 {
		t.Fatalf("invalid source performed I/O or returned content: calls=%d err=%v", calls.Load(), err)
	}
}

func TestReadSourceRejectsBrokenNativeChain(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sourceTestFixture)
	}{
		{"wrong commit", func(f *sourceTestFixture) { f.commit["sha"] = strings.Repeat("d", 40) }},
		{"commit other repository", func(f *sourceTestFixture) {
			f.commit["url"] = strings.Replace(f.commit["url"].(string), "octo/pipeline", "octo/other", 1)
		}},
		{"root pointer invalid sha", func(f *sourceTestFixture) { f.commit["tree"].(map[string]any)["sha"] = "main" }},
		{"root pointer escape", func(f *sourceTestFixture) { f.commit["tree"].(map[string]any)["url"] = "https://evil.test/tree" }},
		{"wrong root tree", func(f *sourceTestFixture) { f.root["sha"] = strings.Repeat("d", 40) }},
		{"wrong subtree", func(f *sourceTestFixture) { f.dir["sha"] = strings.Repeat("d", 40) }},
		{"tree other repository", func(f *sourceTestFixture) {
			f.dir["url"] = strings.Replace(f.dir["url"].(string), "octo/pipeline", "octo/other", 1)
		}},
		{"truncated tree", func(f *sourceTestFixture) { f.dir["truncated"] = true }},
		{"missing truncated", func(f *sourceTestFixture) { delete(f.dir, "truncated") }},
		{"null truncated", func(f *sourceTestFixture) { f.dir["truncated"] = nil }},
		{"null tree", func(f *sourceTestFixture) { f.dir["tree"] = nil }},
		{"missing path", func(f *sourceTestFixture) { f.dir["tree"] = []any{} }},
		{"path case drift", func(f *sourceTestFixture) { f.entry()["path"] = "INPUT.md" }},
		{"recursive tree entry", func(f *sourceTestFixture) { f.entry()["path"] = "docs/input.md" }},
		{"duplicate path", func(f *sourceTestFixture) { f.dir["tree"] = []any{f.entry(), f.entry()} }},
		{"invalid entry sha", func(f *sourceTestFixture) { f.entry()["sha"] = "main" }},
		{"parent symlink", func(f *sourceTestFixture) { f.directory()["mode"], f.directory()["type"] = "120000", "blob" }},
		{"parent submodule", func(f *sourceTestFixture) { f.directory()["mode"], f.directory()["type"] = "160000", "commit" }},
		{"parent regular file", func(f *sourceTestFixture) { f.directory()["mode"], f.directory()["type"] = "100644", "blob" }},
		{"parent tree url escape", func(f *sourceTestFixture) { f.directory()["url"] = "https://evil.test/tree" }},
		{"terminal directory", func(f *sourceTestFixture) { f.entry()["mode"], f.entry()["type"] = "040000", "tree" }},
		{"terminal symlink", func(f *sourceTestFixture) { f.entry()["mode"] = "120000" }},
		{"terminal submodule", func(f *sourceTestFixture) { f.entry()["mode"], f.entry()["type"] = "160000", "commit" }},
		{"terminal unknown mode", func(f *sourceTestFixture) { f.entry()["mode"] = "100664" }},
		{"terminal blob url escape", func(f *sourceTestFixture) { f.entry()["url"] = "https://evil.test/blob" }},
		{"tree missing size", func(f *sourceTestFixture) { delete(f.entry(), "size") }},
		{"tree null size", func(f *sourceTestFixture) { f.entry()["size"] = nil }},
		{"tree negative size", func(f *sourceTestFixture) { f.entry()["size"] = -1 }},
		{"tree oversized", func(f *sourceTestFixture) { f.entry()["size"] = maxSourceBytes + 1 }},
		{"blob missing size", func(f *sourceTestFixture) { delete(f.blob, "size") }},
		{"blob null size", func(f *sourceTestFixture) { f.blob["size"] = nil }},
		{"blob mismatched size", func(f *sourceTestFixture) { f.blob["size"] = len(f.data) + 1 }},
		{"wrong blob", func(f *sourceTestFixture) { f.blob["sha"] = strings.Repeat("d", 40) }},
		{"blob other repository", func(f *sourceTestFixture) {
			f.blob["url"] = strings.Replace(f.blob["url"].(string), "octo/pipeline", "octo/other", 1)
		}},
		{"blob wrong encoding", func(f *sourceTestFixture) { f.blob["encoding"] = "utf-8" }},
		{"blob missing content", func(f *sourceTestFixture) { delete(f.blob, "content") }},
		{"blob null content", func(f *sourceTestFixture) { f.blob["content"] = nil }},
		{"blob invalid base64", func(f *sourceTestFixture) { f.blob["content"] = "!" }},
		{"blob non-base64 whitespace", func(f *sourceTestFixture) { f.blob["content"] = "aGVs bG8K" }},
		{"blob changed contents", func(f *sourceTestFixture) { f.blob["content"] = base64.StdEncoding.EncodeToString([]byte("jello\n")) }},
		{"content digest drift", func(f *sourceTestFixture) { f.ref.SHA256 = strings.Repeat("d", 64) }},
		{"blob git hash framing", func(f *sourceTestFixture) {
			wrong := fmt.Sprintf("%x", sha1.Sum(f.data))
			f.entry()["sha"], f.entry()["url"] = wrong, sourceTestURL("blobs", wrong)
			f.blob["sha"], f.blob["url"] = wrong, sourceTestURL("blobs", wrong)
			f.objects[sourceTestPrefix+"blobs/"+wrong] = f.blob
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newSourceTestFixture([]byte("hello\n"))
			test.mutate(f)
			g, _ := testServerFixtureGateway(t, f.handler(t))
			got, err := g.ReadSource(context.Background(), f.ref)
			if err == nil || got.Bytes != nil || got.BlobSHA != "" || got.Reference != (protocol.SourceReference{}) {
				t.Fatalf("broken source chain exposed partial content: %+v err=%v", got, err)
			}
		})
	}
}

func TestReadSourceReadFailuresNeverFallback(t *testing.T) {
	for _, stage := range []string{"commits", "trees", "blobs"} {
		for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusTooManyRequests, http.StatusInternalServerError} {
			t.Run(fmt.Sprintf("%s/%d", stage, status), func(t *testing.T) {
				f := newSourceTestFixture([]byte("hello\n"))
				native := f.handler(t)
				var failures atomic.Int64
				g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) {
					if strings.Contains(r.URL.Path, "/git/"+stage+"/") {
						failures.Add(1)
						w.WriteHeader(status)
						_, _ = w.Write([]byte("secret-source-content synthetic-test-bearer"))
						return
					}
					native(w, r)
				})
				got, err := g.ReadSource(context.Background(), f.ref)
				if err == nil || got.Bytes != nil || failures.Load() != 1 || strings.Contains(err.Error(), "secret-source-content") || strings.Contains(err.Error(), "synthetic-test-bearer") {
					t.Fatalf("source failure retried, leaked or fell back: %+v err=%v failures=%d", got, err, failures.Load())
				}
			})
		}
	}
}

func TestReadSourceRejectsAmbiguousNativeJSON(t *testing.T) {
	for _, body := range []string{
		`{"sha":"a","sha":"b"}`,
		`{"sha":"a","SHA":"b"}`,
		`{"tree":{"sha":"a","SHA":"b"}}`,
		`{"tree":{"sha":"a","sha":"b"}}`,
		`null`, `{} {}`, `{"sha":"\ud800"}`,
	} {
		t.Run(body, func(t *testing.T) {
			g, _ := testServerFixtureGateway(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
			if got, err := g.ReadSource(context.Background(), newSourceTestFixture([]byte("hello\n")).ref); err == nil || got.Bytes != nil {
				t.Fatalf("ambiguous source JSON accepted: %+v err=%v", got, err)
			}
		})
	}
}
