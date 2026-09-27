package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

const testRepository = "example/control-plane"

func testTask() map[string]any {
	return map[string]any{
		"id": "TASK-090", "title": "Fix regional permissions",
		"goal": "Check region boundaries.\nPreserve the audit trail.", "status": "READY",
		"requirements": []any{"REQ-071"},
		"acceptance":   []any{map[string]any{"id": "AC-01", "description": "A user cannot read another region's data."}},
	}
}

func mockRunner(t *testing.T, runner func([]string) (string, error)) {
	t.Helper()
	previous := runGH
	runGH = runner
	t.Cleanup(func() { runGH = previous })
}

func forbidGH(t *testing.T) {
	t.Helper()
	mockRunner(t, func(args []string) (string, error) {
		t.Fatalf("unexpected gh call: %v", args)
		return "", errors.New("unexpected gh call")
	})
}

func encode(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func issue(body string, number int, state string) map[string]any {
	return map[string]any{"number": number, "body": body, "title": "User's custom title", "state": state,
		"html_url": fmt.Sprintf("https://github.com/example/control-plane/issues/%d", number)}
}

func mustPlan(t *testing.T, task map[string]any) map[string]any {
	t.Helper()
	plan, err := PlanIssue(testRepository, task)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func flag(t *testing.T, args []string, name string) string {
	t.Helper()
	for index, item := range args {
		if item == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	t.Fatalf("missing %s in %v", name, args)
	return ""
}

func has(args []string, item string) bool {
	for _, arg := range args {
		if arg == item {
			return true
		}
	}
	return false
}

func TestPlanIsDeterministicAndOffline(t *testing.T) {
	forbidGH(t)
	task := testTask()
	before := encode(t, task)
	first, second := mustPlan(t, task), mustPlan(t, task)
	if !reflect.DeepEqual(first, second) || before != encode(t, task) || first["action"] != "plan" {
		t.Fatal("plan changed inputs or is not deterministic")
	}
	body := first["body"].(string)
	for _, expected := range []string{"<!-- github-project-control:TASK-090 -->", task["goal"].(string), "AC-01", "REQ-071"} {
		if !strings.Contains(body, expected) {
			t.Errorf("body omits %q", expected)
		}
	}
}

func TestDryRunIsOfflineEvenWithRemoteAuthorization(t *testing.T) {
	forbidGH(t)
	for _, allow := range []bool{false, true} {
		result, err := SyncIssue(testRepository, testTask(), false, allow)
		if err != nil || result["action"] != "plan" {
			t.Fatalf("unexpected dry run: %v, %v", result, err)
		}
	}
}

func TestApplyRequiresRemoteAuthorization(t *testing.T) {
	forbidGH(t)
	if _, err := SyncIssue(testRepository, testTask(), true, false); err == nil {
		t.Fatal("remote write was allowed")
	}
}

func TestRejectsRepositoryAndTaskIDInjection(t *testing.T) {
	forbidGH(t)
	for _, repository := range []string{"--repo", "a/b/c", "a/b;touch /tmp/x", "a/..", "https://github.com/a/b", "a/b\n"} {
		if _, err := PlanIssue(repository, testTask()); err == nil {
			t.Errorf("accepted invalid repository %q", repository)
		}
	}
	for _, taskID := range []string{"bad-id", "TASK X", "../TASK", "TASK-01\n", "TASK$(id)"} {
		task := testTask()
		task["id"] = taskID
		if _, err := PlanIssue(testRepository, task); err == nil {
			t.Errorf("accepted invalid task ID %q", taskID)
		}
	}
}

func TestTextCannotInjectReservedMarkers(t *testing.T) {
	task := testTask()
	task["goal"] = "Example <!-- github-project-control:TASK-090 -->"
	plan := mustPlan(t, task)
	body := plan["body"].(string)
	if strings.Count(body, plan["marker"].(string)) != 1 || !strings.Contains(body, "&lt;!-- github-project-control:TASK-090 -->") {
		t.Fatal("task text manufactured control markers")
	}
}

func TestRequirementObjectsAndTypedSlices(t *testing.T) {
	task := testTask()
	task["requirements"] = []map[string]any{{"id": "REQ-071", "source": "docs/spec.md#access"}}
	if !strings.Contains(mustPlan(t, task)["body"].(string), "docs/spec.md#access") {
		t.Fatal("reference metadata lost")
	}
	task["requirements"] = []string{"REQ-071"}
	task["acceptance"] = []map[string]any{{"id": "AC-01", "description": "Readable"}}
	mustPlan(t, task)
}

func TestInvalidTaskShapesFail(t *testing.T) {
	forbidGH(t)
	if _, err := PlanIssue(testRepository, nil); err == nil {
		t.Fatal("nil task accepted")
	}
	for _, change := range []map[string]any{{"goal": nil}, {"title": "x\x00y"}, {"acceptance": []any{"bad"}},
		{"requirements": "REQ-01"}, {"requirements": []any{map[string]any{}}}, {"requirements": []any{map[string]any{"id": "REQ-01", "bad": make(chan int)}}}} {
		task := testTask()
		for key, value := range change {
			task[key] = value
		}
		if _, err := PlanIssue(testRepository, task); err == nil {
			t.Errorf("accepted invalid change %v", change)
		}
	}
}

func TestCreateUsesAllPagesAndBodyFile(t *testing.T) {
	task := testTask()
	task["title"] = "Title with `echo x` and $(other)"
	plan := mustPlan(t, task)
	calls := 0
	var bodyPath string
	mockRunner(t, func(args []string) (string, error) {
		calls++
		if args[0] == "api" {
			if !has(args, "--paginate") || !has(args, "--slurp") || !strings.Contains(args[1], "state=all") || flag(t, args, "--method") != "GET" {
				t.Fatalf("unsafe/truncated discovery: %v", args)
			}
			pr := issue(plan["body"].(string), 8, "open")
			pr["pull_request"] = map[string]any{"url": "not-an-issue"}
			return encode(t, [][]any{{issue("ordinary issue", 9, "open")}, {pr}}), nil
		}
		if !reflect.DeepEqual(args[:2], []string{"issue", "create"}) || flag(t, args, "--title") != plan["title"] {
			t.Fatalf("unexpected mutation: %v", args)
		}
		bodyPath = flag(t, args, "--body-file")
		body, err := os.ReadFile(bodyPath)
		if err != nil || string(body) != plan["body"] {
			t.Fatalf("body file corrupted: %s, %v", body, err)
		}
		return "https://github.com/example/control-plane/issues/10\n", nil
	})
	result, err := SyncIssue(testRepository, task, true, true)
	if err != nil || result["action"] != "created" || result["issue_number"] != 10 || calls != 2 {
		t.Fatalf("unexpected create result: %v, %v (%d calls)", result, err, calls)
	}
	if _, err := os.Stat(bodyPath); !os.IsNotExist(err) {
		t.Fatal("temporary body file was not removed")
	}
}

func TestUpdatePreservesUserTextTitleAndClosedState(t *testing.T) {
	task := testTask()
	original := mustPlan(t, task)
	task["goal"], task["status"] = "Changed goal\nAnother line", "DONE"
	prefix, suffix := "User introduction\r\n\r\n", "\nUser notes stay verbatim.\n- [x] Manually tracked item\n"
	body := prefix + original["body"].(string) + suffix
	expected := prefix + mustPlan(t, task)["body"].(string) + suffix
	writes := 0
	mockRunner(t, func(args []string) (string, error) {
		if args[0] == "api" {
			return encode(t, [][]any{{issue(body, 9, "closed")}}), nil
		}
		writes++
		if !reflect.DeepEqual(args[:3], []string{"issue", "edit", "9"}) || has(args, "--title") || has(args, "close") {
			t.Fatalf("unexpected mutation: %v", args)
		}
		written, err := os.ReadFile(flag(t, args, "--body-file"))
		if err != nil || string(written) != expected {
			t.Fatalf("user text was changed: %q, %v", written, err)
		}
		return "https://github.com/example/control-plane/issues/9\n", nil
	})
	result, err := SyncIssue(testRepository, task, true, true)
	if err != nil || result["action"] != "updated" || result["issue_state"] != "closed" || writes != 1 {
		t.Fatalf("unexpected update: %v, %v", result, err)
	}
}

func TestRetryIsNoopAndFindsClosedIssues(t *testing.T) {
	plan := mustPlan(t, testTask())
	calls := 0
	mockRunner(t, func(args []string) (string, error) {
		calls++
		if args[0] != "api" {
			t.Fatalf("unexpected write: %v", args)
		}
		return encode(t, [][]any{{issue("unrelated", 1, "open")}, {issue(plan["body"].(string), 12, "closed")}}), nil
	})
	result, err := SyncIssue(testRepository, testTask(), true, true)
	if err != nil || result["action"] != "unchanged" || result["issue_number"] != 12 || calls != 1 {
		t.Fatalf("unexpected retry: %v, %v", result, err)
	}
}

func TestLostCreateResponseDoesNotCreateTwice(t *testing.T) {
	remoteIssues := []any{}
	creates := 0
	mockRunner(t, func(args []string) (string, error) {
		if args[0] == "api" {
			return encode(t, [][]any{remoteIssues}), nil
		}
		if !reflect.DeepEqual(args[:2], []string{"issue", "create"}) {
			t.Fatalf("unexpected write: %v", args)
		}
		creates++
		body, err := os.ReadFile(flag(t, args, "--body-file"))
		if err != nil {
			t.Fatal(err)
		}
		remoteIssues = append(remoteIssues, issue(string(body), 23, "open"))
		return "", errors.New("response lost after successful remote write")
	})
	if _, err := SyncIssue(testRepository, testTask(), true, true); err == nil {
		t.Fatal("expected lost response error")
	}
	result, err := SyncIssue(testRepository, testTask(), true, true)
	if err != nil || result["action"] != "unchanged" || result["issue_number"] != 23 || creates != 1 {
		t.Fatalf("retry duplicated write: %v, %v, %d creates", result, err, creates)
	}
}

func TestLegacyMarkerAppendsWithoutDestroyingUserText(t *testing.T) {
	plan := mustPlan(t, testTask())
	body := "Old human-authored description\n" + plan["marker"].(string) + "\nUser notes"
	mockRunner(t, func(args []string) (string, error) {
		if args[0] == "api" {
			return encode(t, [][]any{{issue(body, 9, "open")}}), nil
		}
		return "https://github.com/example/control-plane/issues/9", nil
	})
	result, err := SyncIssue(testRepository, testTask(), true, true)
	if err != nil || !strings.HasPrefix(result["body"].(string), body+"\n\n") || strings.Count(result["body"].(string), plan["marker"].(string)) != 1 {
		t.Fatalf("legacy text damaged: %v, %v", result, err)
	}
}

func TestDuplicateIssueMarkersFailBeforeWrite(t *testing.T) {
	plan := mustPlan(t, testTask())
	calls := 0
	mockRunner(t, func(args []string) (string, error) {
		calls++
		return encode(t, [][]any{{issue(plan["body"].(string), 1, "open")}, {issue(plan["body"].(string), 2, "closed")}}), nil
	})
	if _, err := SyncIssue(testRepository, testTask(), true, true); err == nil || !strings.Contains(err.Error(), "duplicate") || calls != 1 {
		t.Fatalf("duplicate marker not rejected: %v (%d calls)", err, calls)
	}
}

func TestDuplicateMarkerInOneIssueFailsBeforeWrite(t *testing.T) {
	plan := mustPlan(t, testTask())
	calls := 0
	mockRunner(t, func(args []string) (string, error) {
		calls++
		return encode(t, [][]any{{issue(plan["body"].(string)+"\n"+plan["marker"].(string), 1, "open")}}), nil
	})
	if _, err := SyncIssue(testRepository, testTask(), true, true); err == nil || !strings.Contains(err.Error(), "duplicate") || calls != 1 {
		t.Fatalf("duplicate marker not rejected: %v (%d calls)", err, calls)
	}
}

func TestMalformedBlocksNeverReplaceBody(t *testing.T) {
	marker, begin, end := markers("TASK-090")
	for _, body := range []string{marker + begin, marker + end, marker + end + begin, marker + begin + begin + end, begin + marker + end} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			mockRunner(t, func(args []string) (string, error) {
				calls++
				return encode(t, [][]any{{issue(body, 1, "open")}}), nil
			})
			if _, err := SyncIssue(testRepository, testTask(), true, true); err == nil || calls != 1 {
				t.Fatalf("malformed block not rejected: %v (%d calls)", err, calls)
			}
		})
	}
}

func TestInvalidRemoteDataFailsWithoutWrite(t *testing.T) {
	for _, output := range []string{"not json", "{}", `[{"number":1}]`, "[[null]]", "[] garbage", `[[{"body":42}]]`} {
		t.Run(output, func(t *testing.T) {
			calls := 0
			mockRunner(t, func(args []string) (string, error) { calls++; return output, nil })
			if _, err := SyncIssue(testRepository, testTask(), true, true); err == nil || calls != 1 {
				t.Fatalf("invalid data not rejected: %v (%d calls)", err, calls)
			}
		})
	}
}

func TestGHFailureIsReturnedWithoutInternalRetry(t *testing.T) {
	expected := errors.New("remote unavailable")
	calls := 0
	mockRunner(t, func(args []string) (string, error) { calls++; return "", expected })
	if _, err := SyncIssue(testRepository, testTask(), true, true); !errors.Is(err, expected) || calls != 1 {
		t.Fatalf("error lost or operation retried: %v (%d calls)", err, calls)
	}
}

func TestInspectPRReturnsFactsWithoutInventingAcceptance(t *testing.T) {
	facts := map[string]any{"headRefOid": strings.Repeat("a", 40), "baseRefOid": strings.Repeat("b", 40),
		"state": "OPEN", "mergeCommit": nil, "statusCheckRollup": []any{}, "reviewDecision": "APPROVED"}
	mockRunner(t, func(args []string) (string, error) {
		if !reflect.DeepEqual(args[:3], []string{"pr", "view", "42"}) || flag(t, args, "--repo") != testRepository {
			t.Fatalf("unexpected PR call: %v", args)
		}
		fields := strings.Split(flag(t, args, "--json"), ",")
		for field := range facts {
			if !has(fields, field) {
				t.Errorf("missing PR field %s", field)
			}
		}
		return encode(t, facts), nil
	})
	result, err := InspectPR(testRepository, 42)
	if err != nil || !reflect.DeepEqual(result, facts) {
		t.Fatalf("unexpected PR facts: %v, %v", result, err)
	}
	if _, exists := result["accepted"]; exists {
		t.Fatal("adapter invented acceptance")
	}
}

func TestInspectPRRejectsInvalidIdentifiersBeforeNetwork(t *testing.T) {
	forbidGH(t)
	for _, number := range []int{0, -1} {
		if _, err := InspectPR(testRepository, number); err == nil {
			t.Errorf("accepted PR number %d", number)
		}
	}
	if _, err := InspectPR("a/b/c", 1); err == nil {
		t.Fatal("accepted malformed PR repository")
	}
}
