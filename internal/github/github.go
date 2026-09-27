// Package github projects authoritative local task records into GitHub Issues.
// Planning never accesses the network. Applying changes requires both explicit
// flags. Callers must serialize writes: Issues do not enforce unique markers or
// offer this adapter an atomic replacement of a body edited concurrently.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)
	taskIDPattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_-]*$`)
	issueURLPattern   = regexp.MustCompile(`/issues/([1-9][0-9]*)/?$`)
	// Tests replace this runner without invoking gh or accessing GitHub.
	runGH = defaultRunGH
)

const reservedPrefix = "<!-- github-project-control:"
const prFields = "number,url,title,headRefOid,baseRefOid,headRefName,baseRefName,state,isDraft,mergeCommit,potentialMergeCommit,mergedAt,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup"

func validateRepository(repository string) error {
	parts := strings.Split(repository, "/")
	if !repositoryPattern.MatchString(repository) || len(parts) != 2 || parts[1] == "." || parts[1] == ".." {
		return fmt.Errorf("repository must be an owner/name pair")
	}
	return nil
}

func text(value any, field string) (string, error) {
	valueText, ok := value.(string)
	if !ok || strings.TrimSpace(valueText) == "" || strings.ContainsRune(valueText, '\x00') {
		return "", fmt.Errorf("%s must be a nonempty string without NUL", field)
	}
	// Descriptions are data and cannot manufacture adapter control markers.
	return strings.ReplaceAll(valueText, reservedPrefix, "&lt;!-- github-project-control:"), nil
}

func markers(taskID string) (string, string, string) {
	return fmt.Sprintf("<!-- github-project-control:%s -->", taskID),
		fmt.Sprintf("<!-- github-project-control:%s:begin -->", taskID),
		fmt.Sprintf("<!-- github-project-control:%s:end -->", taskID)
}

func array(value any, field string) ([]any, error) {
	switch values := value.(type) {
	case []any:
		return values, nil
	case []string:
		result := make([]any, len(values))
		for index, item := range values {
			result[index] = item
		}
		return result, nil
	case []map[string]any:
		result := make([]any, len(values))
		for index, item := range values {
			result[index] = item
		}
		return result, nil
	default:
		return nil, fmt.Errorf("%s must be a list", field)
	}
}

// PlanIssue returns a deterministic, rebuildable projection without invoking gh.
func PlanIssue(repository string, task map[string]any) (map[string]any, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	taskID, ok := task["id"].(string)
	if !ok || !taskIDPattern.MatchString(taskID) {
		return nil, fmt.Errorf("task id must match [A-Z][A-Z0-9_-]*")
	}
	title, err := text(task["title"], "task.title")
	if err != nil {
		return nil, err
	}
	goal, err := text(task["goal"], "task.goal")
	if err != nil {
		return nil, err
	}
	status, err := text(task["status"], "task.status")
	if err != nil {
		return nil, err
	}
	acceptance, err := array(task["acceptance"], "task.acceptance")
	if err != nil {
		return nil, err
	}
	requirements, err := array(task["requirements"], "task.requirements")
	if err != nil {
		return nil, err
	}
	var acceptanceLines []string
	for index, value := range acceptance {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("acceptance entries must be objects")
		}
		identifier, err := text(item["id"], fmt.Sprintf("acceptance[%d].id", index))
		if err != nil {
			return nil, err
		}
		description, err := text(item["description"], fmt.Sprintf("acceptance[%d].description", index))
		if err != nil {
			return nil, err
		}
		acceptanceLines = append(acceptanceLines, fmt.Sprintf("- **%s**: %s", identifier, description))
	}
	var requirementLines []string
	for index, value := range requirements {
		if item, ok := value.(map[string]any); ok {
			if _, err := text(item["id"], fmt.Sprintf("requirements[%d].id", index)); err != nil {
				return nil, err
			}
			// Disable HTML escaping so source text and paths remain readable; map
			// keys are sorted by encoding/json, preserving deterministic output.
			var buffer bytes.Buffer
			encoder := json.NewEncoder(&buffer)
			encoder.SetEscapeHTML(false)
			if err := encoder.Encode(item); err != nil {
				return nil, fmt.Errorf("requirement references must be JSON serializable: %w", err)
			}
			value = strings.TrimSuffix(buffer.String(), "\n")
		}
		valueText, err := text(value, fmt.Sprintf("requirements[%d]", index))
		if err != nil {
			return nil, err
		}
		requirementLines = append(requirementLines, "- "+valueText)
	}
	if len(requirementLines) == 0 {
		requirementLines = []string{"- None specified."}
	}
	if len(acceptanceLines) == 0 {
		acceptanceLines = []string{"- None specified."}
	}
	marker, begin, end := markers(taskID)
	lines := []string{begin, fmt.Sprintf("## %s: %s", taskID, title), "",
		fmt.Sprintf("Task status: **%s**", status), "", "### Goal", goal, "", "### Requirements"}
	lines = append(lines, requirementLines...)
	lines = append(lines, "", "### Acceptance criteria")
	lines = append(lines, acceptanceLines...)
	lines = append(lines, "", "This Issue is a rebuildable projection of the repository task record.",
		"Issue state and checkboxes are not acceptance evidence.", end)
	managed := strings.Join(lines, "\n")
	return map[string]any{
		"action": "plan", "repository": repository, "task_id": taskID,
		"title": fmt.Sprintf("[%s] %s", taskID, title), "marker": marker,
		"managed_body": managed, "body": marker + "\n" + managed + "\n",
	}, nil
}

func defaultRunGH(arguments []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	// exec.CommandContext passes each argument separately on supported platforms.
	// No shell, credential management, or authentication command is involved.
	command := exec.CommandContext(ctx, "gh", arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("gh could not complete: %w", ctx.Err())
		}
		detail := strings.TrimSpace(stderr.String())
		if len(detail) > 1000 {
			detail = detail[:1000]
		}
		return "", fmt.Errorf("gh failed: %w: %s", err, detail)
	}
	return stdout.String(), nil
}

func jsonOutput(arguments []string) (any, error) {
	output, err := runGH(arguments)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("gh returned invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("gh returned extra data after JSON")
	}
	return result, nil
}

func issueBody(issue map[string]any) (string, error) {
	if issue["body"] == nil {
		return "", nil
	}
	body, ok := issue["body"].(string)
	if !ok {
		return "", fmt.Errorf("gh returned an invalid issue body")
	}
	return body, nil
}

func findIssue(repository, marker string) (map[string]any, error) {
	data, err := jsonOutput([]string{"api", "repos/" + repository + "/issues?state=all&per_page=100",
		"--method", "GET", "--paginate", "--slurp"})
	if err != nil {
		return nil, err
	}
	pages, ok := data.([]any)
	if !ok {
		return nil, fmt.Errorf("gh returned an unexpected paginated issues response")
	}
	var match map[string]any
	for _, rawPage := range pages {
		page, ok := rawPage.([]any)
		if !ok {
			return nil, fmt.Errorf("gh returned an unexpected paginated issues response")
		}
		for _, rawIssue := range page {
			issue, ok := rawIssue.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("gh returned an invalid issue record")
			}
			// GitHub's REST issues endpoint also returns pull requests.
			if _, isPR := issue["pull_request"]; isPR {
				continue
			}
			body, err := issueBody(issue)
			if err != nil {
				return nil, err
			}
			if strings.Contains(body, marker) {
				if strings.Count(body, marker) != 1 {
					return nil, fmt.Errorf("duplicate task markers in one issue; refusing to write")
				}
				if match != nil {
					return nil, fmt.Errorf("duplicate task markers across issues; refusing to write")
				}
				match = issue
			}
		}
	}
	return match, nil
}

func replaceManaged(body string, plan map[string]any) (string, error) {
	marker, begin, end := markers(plan["task_id"].(string))
	if strings.Count(body, marker) != 1 {
		return "", fmt.Errorf("issue must contain exactly one task marker")
	}
	startCount, endCount := strings.Count(body, begin), strings.Count(body, end)
	if startCount == 0 && endCount == 0 {
		separator := "\n\n"
		if strings.HasSuffix(body, "\n\n") {
			separator = ""
		} else if strings.HasSuffix(body, "\n") {
			separator = "\n"
		}
		return body + separator + plan["managed_body"].(string) + "\n", nil
	}
	start, endStart := strings.Index(body, begin), strings.Index(body, end)
	if startCount != 1 || endCount != 1 || endStart < start {
		return "", fmt.Errorf("malformed managed block; refusing to replace user text")
	}
	stop := endStart + len(end)
	markerPosition := strings.Index(body, marker)
	if start <= markerPosition && markerPosition < stop {
		return "", fmt.Errorf("task marker cannot be inside the managed block")
	}
	return body[:start] + plan["managed_body"].(string) + body[stop:], nil
}

func writeBody(arguments []string, body string) (string, error) {
	directory, err := os.MkdirTemp("", "github-project-control-")
	if err != nil {
		return "", fmt.Errorf("could not prepare the Issue body file: %w", err)
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "issue-body.md")
	// WriteFile closes the file before gh opens it, including on Windows.
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		return "", fmt.Errorf("could not prepare the Issue body file: %w", err)
	}
	return runGH(append(arguments, "--body-file", path))
}

// SyncIssue is offline unless apply is true. Remote writes require allowRemote
// as well. Existing Issue state/title/labels and unmanaged body text are kept.
// If a write's response is lost, the next call rediscovers the marker before
// deciding whether to create. Concurrent writers require caller serialization.
func SyncIssue(repository string, task map[string]any, apply, allowRemote bool) (map[string]any, error) {
	plan, err := PlanIssue(repository, task)
	if err != nil {
		return nil, err
	}
	if !apply {
		return plan, nil
	}
	if !allowRemote {
		return nil, fmt.Errorf("remote writes require apply=true and allowRemote=true")
	}
	issue, err := findIssue(repository, plan["marker"].(string))
	if err != nil {
		return nil, err
	}
	if issue == nil {
		output, err := writeBody([]string{"issue", "create", "--repo", repository, "--title", plan["title"].(string)}, plan["body"].(string))
		if err != nil {
			return nil, err
		}
		output = strings.TrimSpace(output)
		plan["action"], plan["url"] = "created", output
		if match := issueURLPattern.FindStringSubmatch(output); match != nil {
			if number, err := strconv.Atoi(match[1]); err == nil {
				plan["issue_number"] = number
			}
		}
		return plan, nil
	}
	value, ok := issue["number"].(json.Number)
	if !ok {
		return nil, fmt.Errorf("gh returned an invalid issue number")
	}
	number, err := strconv.Atoi(string(value))
	if err != nil || number <= 0 {
		return nil, fmt.Errorf("gh returned an invalid issue number")
	}
	oldBody, err := issueBody(issue)
	if err != nil {
		return nil, err
	}
	newBody, err := replaceManaged(oldBody, plan)
	if err != nil {
		return nil, err
	}
	plan["action"], plan["issue_number"], plan["issue_state"], plan["body"] = "unchanged", number, issue["state"], newBody
	plan["url"] = issue["html_url"]
	if plan["url"] == nil {
		plan["url"] = issue["url"]
	}
	if newBody != oldBody {
		if _, err := writeBody([]string{"issue", "edit", strconv.Itoa(number), "--repo", repository}, newBody); err != nil {
			return nil, err
		}
		plan["action"] = "updated"
	}
	return plan, nil
}

// InspectPR returns read-only PR facts; it does not infer acceptance from checks
// or reviewDecision. The requested fields are documented by gh pr view --help.
func InspectPR(repository string, prNumber int) (map[string]any, error) {
	if err := validateRepository(repository); err != nil {
		return nil, err
	}
	if prNumber <= 0 {
		return nil, fmt.Errorf("prNumber must be a positive integer")
	}
	value, err := jsonOutput([]string{"pr", "view", strconv.Itoa(prNumber), "--repo", repository, "--json", prFields})
	if err != nil {
		return nil, err
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("gh returned an invalid pull request response")
	}
	return result, nil
}
