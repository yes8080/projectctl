package github

import (
	"context"
	"crypto/sha1" // Git's blob object identity, not an authorization digest.
	"encoding/base64"
	"fmt"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

const (
	maxSourceBytes     = 4 << 20
	maxSourcePathBytes = 4096
	maxSourceDepth     = 64
)

// SourceBlob is a disposable observation of one regular file in the Gateway's
// repository. Bytes are the exact Git blob contents, not rendered Markdown.
type SourceBlob struct {
	Reference protocol.SourceReference
	BlobSHA   string
	Bytes     []byte
}

type nativeSourcePointer struct {
	SHA string `json:"sha"`
	URL string `json:"url"`
}

type nativeSourceEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	URL  string `json:"url"`
	Size *int64 `json:"size"`
}

func sourceHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func sourcePath(value string) bool {
	if value == "" || len(value) > maxSourcePathBytes || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.ContainsAny(value, "\\%:") {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return len(strings.Split(value, "/")) <= maxSourceDepth
}

func (g *Gateway) sourceURL(kind, sha, value string) bool {
	if value == g.apiURL(g.repoPath()+"/git/"+kind+"/"+sha) {
		return true
	}
	// GitHub's documented tree representation also uses this legacy URL.
	return kind == "trees" && value == g.apiURL(g.repoPath()+"/trees/"+sha)
}

func (g *Gateway) sourceObject(ctx context.Context, kind, sha string, total *int, out any) error {
	data, _, err := g.requestWithAccept(ctx, http.MethodGet, g.endpoint(g.repoPath()+"/git/"+kind+"/"+sha), nil, "application/vnd.github+json")
	if err != nil {
		return err
	}
	*total += len(data)
	if *total > maxTraversalBytes {
		return fmt.Errorf("source traversal exceeds safety bound")
	}
	return decodeAPI(data, out)
}

// ReadSource resolves a full commit SHA and exact repository-relative path using
// Git commit/tree/blob reads only. It never resolves a branch, tag, local file,
// symlink or submodule, follows a response URL, or uses a recursive tree listing.
// Tree/commit identity relies on the authenticated native API; blob identity is
// additionally recomputed from Git's object framing and its exact byte content.
func (g *Gateway) ReadSource(ctx context.Context, ref protocol.SourceReference) (SourceBlob, error) {
	var zero SourceBlob
	if !sourceHex(ref.Commit, 40) || !sourceHex(ref.SHA256, 64) || !sourcePath(ref.Path) {
		return zero, fmt.Errorf("source requires exact commit, normalized path and content digest")
	}
	ctx, err := g.bindCredential(ctx)
	if err != nil {
		return zero, err
	}
	total := 0
	var commit struct {
		SHA  string              `json:"sha"`
		URL  string              `json:"url"`
		Tree nativeSourcePointer `json:"tree"`
	}
	if err := g.sourceObject(ctx, "commits", ref.Commit, &total, &commit); err != nil {
		return zero, err
	}
	if commit.SHA != ref.Commit || !g.sourceURL("commits", ref.Commit, commit.URL) || !sourceHex(commit.Tree.SHA, 40) || !g.sourceURL("trees", commit.Tree.SHA, commit.Tree.URL) {
		return zero, fmt.Errorf("%w: source commit identity or root tree mismatch", ErrConflict)
	}
	parts := strings.Split(ref.Path, "/")
	treeSHA := commit.Tree.SHA
	var selected nativeSourceEntry
	for index, part := range parts {
		var tree struct {
			SHA       string              `json:"sha"`
			URL       string              `json:"url"`
			Tree      []nativeSourceEntry `json:"tree"`
			Truncated *bool               `json:"truncated"`
		}
		if err := g.sourceObject(ctx, "trees", treeSHA, &total, &tree); err != nil {
			return zero, err
		}
		if tree.SHA != treeSHA || !g.sourceURL("trees", treeSHA, tree.URL) || tree.Truncated == nil || *tree.Truncated || tree.Tree == nil || len(tree.Tree) > maxObjects {
			return zero, fmt.Errorf("%w: source tree identity or completeness mismatch", ErrConflict)
		}
		seen := make(map[string]bool, len(tree.Tree))
		found := false
		for _, entry := range tree.Tree {
			if !sourcePath(entry.Path) || strings.Contains(entry.Path, "/") || !sourceHex(entry.SHA, 40) || seen[entry.Path] {
				return zero, fmt.Errorf("%w: invalid or duplicate source tree entry", ErrConflict)
			}
			seen[entry.Path] = true
			if entry.Path == part {
				selected, found = entry, true
			}
		}
		if !found {
			return zero, fmt.Errorf("%w: source path is absent from pinned tree", ErrConflict)
		}
		if index < len(parts)-1 {
			if selected.Type != "tree" || selected.Mode != "040000" || !g.sourceURL("trees", selected.SHA, selected.URL) {
				return zero, fmt.Errorf("%w: source path traverses a non-directory", ErrConflict)
			}
			treeSHA = selected.SHA
			continue
		}
		if selected.Type != "blob" || (selected.Mode != "100644" && selected.Mode != "100755") || selected.Size == nil || *selected.Size < 0 || *selected.Size > maxSourceBytes || !g.sourceURL("blobs", selected.SHA, selected.URL) {
			return zero, fmt.Errorf("%w: source is not a bounded regular file", ErrConflict)
		}
	}
	var blob struct {
		SHA      string  `json:"sha"`
		URL      string  `json:"url"`
		Encoding string  `json:"encoding"`
		Content  *string `json:"content"`
		Size     *int64  `json:"size"`
	}
	if err := g.sourceObject(ctx, "blobs", selected.SHA, &total, &blob); err != nil {
		return zero, err
	}
	if blob.SHA != selected.SHA || !g.sourceURL("blobs", selected.SHA, blob.URL) || blob.Encoding != "base64" || blob.Content == nil || blob.Size == nil || *blob.Size != *selected.Size || *blob.Size < 0 || *blob.Size > maxSourceBytes {
		return zero, fmt.Errorf("%w: source blob identity, encoding or size mismatch", ErrConflict)
	}
	// GitHub inserts line breaks into base64. Only CR/LF are removed; all other
	// invalid bytes and noncanonical padding are rejected by the strict decoder.
	encoded := strings.NewReplacer("\r", "", "\n", "").Replace(*blob.Content)
	if len(encoded) > base64.StdEncoding.EncodedLen(maxSourceBytes) {
		return zero, fmt.Errorf("source blob exceeds content safety bound")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || int64(len(data)) != *blob.Size {
		return zero, fmt.Errorf("%w: invalid source blob content", ErrConflict)
	}
	hash := sha1.New()
	_, _ = fmt.Fprintf(hash, "blob %d\x00", len(data))
	_, _ = hash.Write(data)
	if fmt.Sprintf("%x", hash.Sum(nil)) != selected.SHA || protocol.SHA256(data) != ref.SHA256 {
		return zero, fmt.Errorf("%w: source blob content digest mismatch", ErrConflict)
	}
	return SourceBlob{Reference: ref, BlobSHA: selected.SHA, Bytes: data}, nil
}
