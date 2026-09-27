package startup

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	gh "github.com/yes8080/projectctl/internal/pipeline/github"
	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

type readOnlyTransport struct{ inner http.RoundTripper }

func (r readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return nil, errors.New("live smoke forbids mutation")
	}
	return r.inner.RoundTrip(req)
}

// Explicit developer/acceptance smoke only. Normal tests never read credentials
// or contact GitHub. This test adapter uses the host's existing gh authentication,
// keeps the token in memory and never prints it or command output. Production
// startup does not import os/exec or discover credentials.
func TestLiveReadOnlyStartupInputs(t *testing.T) {
	if os.Getenv("PROJECTCTL_STARTUP_LIVE_READONLY") != "1" {
		t.Skip("explicit read-only live opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	tokenBytes, err := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com").Output()
	if err != nil {
		t.Fatal("existing host credential unavailable")
	}
	token := strings.TrimSpace(string(tokenBytes))
	g, err := gh.New(gh.Config{Project: protocol.Project{Owner: "yes8080", Repo: "projectctl", ControlIssue: 2}, Token: func(context.Context) (string, error) { return token, nil }, HTTPClient: &http.Client{Timeout: 20 * time.Second, Transport: readOnlyTransport{http.DefaultTransport}}})
	if err != nil {
		t.Fatal(err)
	}
	repo, err := g.ReadRepository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if repo.URL != "https://github.com/yes8080/projectctl" || repo.DatabaseID <= 0 || repo.NodeID == "" {
		t.Fatal("repository identity unavailable")
	}
	ref := protocol.SourceReference{Commit: "96cc7ffd49ebffeed80730a715cf9eb6eca13620", Path: "docs/pipeline-design.md", SHA256: "3c5d9585a9fec44455af7a2a4c2a4090a6d78b297cd6f07a196a09bd2a5184c8"}
	for i := 0; i < 2; i++ {
		blob, err := g.ReadSource(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if !matchesBlob(blob, ref) {
			t.Fatal("live source identity mismatch")
		}
		t.Logf("read %d: fixed source %s:%s, bytes=%d, blob=%s", i+1, ref.Commit, ref.Path, len(blob.Bytes), blob.BlobSHA)
	}
	caps := capabilities(g.ProbeCapabilities(ctx, 6))
	for _, name := range capabilityNames {
		t.Logf("capability %s=%s", name, observed(caps[name]).State)
	}
	if !caps["issues_read"].IsSupported() || !caps["comments_read"].IsSupported() || !caps["milestones_read"].IsSupported() {
		t.Fatal("required live read smoke unavailable")
	}
	// Unknown is intentionally not upgraded: this is not a complete live
	// bootstrap, role/host isolation proof, or permission to start a Developer.
	if caps["issue_auto_close"].IsSupported() || caps["protected_merge_gate"].IsSupported() {
		t.Fatal("REST probe unexpectedly claims unproved gate capability")
	}
}
