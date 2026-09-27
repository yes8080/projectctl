package protocol

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Lock the production boundary against accidental persistence, network calls,
// environment-derived authority or dependencies on the prototype ledger.
func TestProductionBoundaryIsPure(t *testing.T) {
	allowed := map[string]bool{"bytes": true, "crypto/sha256": true, "encoding/hex": true, "encoding/json": true, "fmt": true, "io": true, "net/url": true, "path": true, "reflect": true, "regexp": true, "sort": true, "strconv": true, "strings": true, "unicode/utf8": true}
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
			if !allowed[name] {
				t.Errorf("%s imports %s outside the pure protocol boundary", entry.Name(), name)
			}
		}
	}
}

func TestMissingExplicitZeroBudgetIsNotAuthorization(t *testing.T) {
	plan := fixtureRecords()[1].(*Plan)
	data := string(mustEncode(t, plan))
	for _, key := range []string{"max_cost_minor_units", "max_change_requests", "max_defect_fix_issues"} {
		missing := strings.Replace(data, `"`+key+`":0,`, "", 1)
		if missing == data {
			t.Fatalf("test did not remove %s", key)
		}
		if _, err := Decode([]byte(missing)); err == nil {
			t.Fatalf("absent %s treated as authorized zero", key)
		}
	}
}

func TestRebuildOnlyFromObservedRemoteFacts(t *testing.T) {
	policy := fixturePolicy()
	native, ref := observed(t, fixtureContract(), developerIdentity, 100)
	first, err := VerifyGitHubRecord(native, ref)
	if err != nil {
		t.Fatal(err)
	}
	approvalNative, approvalRef := observed(t, approvalFor(t, first, policy), testAuthor, 200)
	first = VerifiedRecord{} // no local capability/cache is needed to reconstruct
	rebuilt, err := VerifyGitHubRecord(native, ref)
	if err != nil {
		t.Fatal(err)
	}
	approval, err := VerifyGitHubRecord(approvalNative, approvalRef)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectApproved([]VerifiedRecord{rebuilt}, approval, policy)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Reference() != ref {
		t.Fatal("reconstruction changed authority")
	}
	native.Body = ""
	if _, err := VerifyGitHubRecord(native, ref); err == nil {
		t.Fatal("missing remote fact replaced by cached authority")
	}
}
