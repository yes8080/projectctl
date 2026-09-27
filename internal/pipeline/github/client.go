// Package github is the GitHub-authoritative pipeline Gateway. It keeps no
// durable local state and never invokes candidate code or a credential CLI.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yes8080/projectctl/internal/pipeline/protocol"
)

const (
	apiVersion        = "2026-03-10"
	maxResponseBytes  = 16 << 20
	maxTraversalBytes = 64 << 20
	maxPages          = 1000
	maxObjects        = 100000
)

var (
	ErrUnavailable = errors.New("GitHub unavailable")
	ErrConflict    = errors.New("conflicting GitHub facts")
	ErrUncertain   = errors.New("remote operation outcome is uncertain; reconcile without retry")
	ErrDenied      = errors.New("write is not authorized")
)

// HTTPError omits response bodies and transport/provider error strings: these
// can contain credentials or private content. Status alone is not capability.
type HTTPError struct {
	Status int
	Method string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("GitHub %s returned HTTP %d", e.Method, e.Status)
}

// Config belongs to the trusted Controller host, never an Agent payload.
// Token must obtain credentials from the host's secret provider. The package
// never reads environment variables/files or sends credentials to candidate code.
type Config struct {
	Project    protocol.Project
	Token      func(context.Context) (string, error)
	HTTPClient *http.Client
	// BaseURL defaults to https://api.github.com. Other origins are accepted only
	// for an explicitly selected loopback httptest server, never for production.
	BaseURL                 string
	AllowLoopbackTestServer bool
	Publisher               protocol.GitHubIdentity
	Authorize               func(context.Context, Intent, protocol.GitHubIdentity) error
}

type Gateway struct {
	project   protocol.Project
	base      *url.URL
	client    *http.Client
	token     func(context.Context) (string, error)
	publisher protocol.GitHubIdentity
	authorize func(context.Context, Intent, protocol.GitHubIdentity) error
}

type credentialContextKey struct{}
type credentialSnapshot struct {
	gateway *Gateway
	token   string
}

// One operation uses one credential snapshot: a rotating provider must not
// authorize /user with identity A and publish the effect with identity B.
func (g *Gateway) bindCredential(ctx context.Context) (context.Context, error) {
	token, err := g.token(ctx)
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, fmt.Errorf("credential provider unavailable")
	}
	return context.WithValue(ctx, credentialContextKey{}, credentialSnapshot{gateway: g, token: token}), nil
}

func New(config Config) (*Gateway, error) {
	if err := config.Project.Validate(); err != nil {
		return nil, err
	}
	if config.Token == nil {
		return nil, fmt.Errorf("authenticated credential provider is required")
	}
	endpoint := config.BaseURL
	if endpoint == "" {
		endpoint = "https://api.github.com"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("invalid API origin")
	}
	production := u.Scheme == "https" && u.Host == "api.github.com"
	test := config.AllowLoopbackTestServer && (u.Scheme == "http" || u.Scheme == "https") && (u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
	if !production && !test {
		return nil, fmt.Errorf("API origin must be GitHub or an explicit loopback test server")
	}
	u.Path = ""
	client := http.Client{Timeout: 30 * time.Second}
	if config.HTTPClient != nil {
		client = *config.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	// A copy prevents a caller's redirect policy from forwarding the bearer token.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Gateway{project: config.Project, base: u, client: &client, token: config.Token, publisher: config.Publisher, authorize: config.Authorize}, nil
}

func (g *Gateway) repoPath() string { return "/repos/" + g.project.Owner + "/" + g.project.Repo }

func (g *Gateway) endpoint(path string) *url.URL { u := *g.base; u.Path = path; return &u }

func (g *Gateway) request(ctx context.Context, method string, u *url.URL, payload any) ([]byte, http.Header, error) {
	if u.Scheme != g.base.Scheme || u.Host != g.base.Host || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return nil, nil, fmt.Errorf("out-of-scope HTTP origin")
	}
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid request payload")
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid HTTP request")
	}
	snapshot, ok := ctx.Value(credentialContextKey{}).(credentialSnapshot)
	token := snapshot.token
	if !ok || snapshot.gateway != g {
		token, err = g.token(ctx)
	}
	if err != nil || token == "" || strings.ContainsAny(token, "\r\n\t ") {
		return nil, nil, fmt.Errorf("credential provider unavailable")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github.raw+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.Header, &HTTPError{Status: resp.StatusCode, Method: method}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, resp.Header, ErrUnavailable
	}
	if len(data) > maxResponseBytes {
		return nil, resp.Header, fmt.Errorf("GitHub response exceeds safety bound")
	}
	return data, resp.Header, nil
}

func (g *Gateway) get(ctx context.Context, path string, out any) error {
	data, _, err := g.request(ctx, http.MethodGet, g.endpoint(path), nil)
	if err != nil {
		return err
	}
	return decodeAPI(data, out)
}

// Pagination is bounded but never silently truncated. All failures discard the
// collection; a partial page is never interpreted as a complete authoritative set.
func (g *Gateway) pages(ctx context.Context, path string, filters url.Values) ([]json.RawMessage, error) {
	u := g.endpoint(path)
	q := url.Values{}
	for key, values := range filters {
		q[key] = append([]string(nil), values...)
	}
	q.Set("per_page", "100")
	q.Set("page", "1")
	u.RawQuery = q.Encode()
	visited := map[string]bool{}
	result := []json.RawMessage{}
	total := 0
	for page := 0; page < maxPages; page++ {
		if visited[u.String()] {
			return nil, fmt.Errorf("pagination cycle")
		}
		visited[u.String()] = true
		data, header, err := g.request(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		total += len(data)
		if total > maxTraversalBytes {
			return nil, fmt.Errorf("pagination byte limit reached")
		}
		var items []json.RawMessage
		if err := decodeAPI(data, &items); err != nil {
			return nil, err
		}
		if items == nil {
			return nil, fmt.Errorf("expected a native JSON array")
		}
		result = append(result, items...)
		if len(result) > maxObjects {
			return nil, fmt.Errorf("pagination object limit reached")
		}
		next, err := nextPage(header.Values("Link"), u, g.base, path, q)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return result, nil
		}
		u = next
	}
	return nil, fmt.Errorf("pagination page limit reached")
}

func nextPage(headers []string, current, origin *url.URL, path string, initial url.Values) (*url.URL, error) {
	var next *url.URL
	for _, header := range headers {
		for _, part := range strings.Split(header, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			end := strings.Index(part, ">")
			if !strings.HasPrefix(part, "<") || end < 1 {
				return nil, fmt.Errorf("malformed pagination Link")
			}
			relations := []string{}
			for _, parameter := range strings.Split(part[end+1:], ";") {
				parameter = strings.TrimSpace(parameter)
				if parameter == "" {
					continue
				}
				key, value, ok := strings.Cut(parameter, "=")
				if !ok {
					return nil, fmt.Errorf("malformed Link parameter")
				}
				if strings.EqualFold(strings.TrimSpace(key), "rel") {
					value = strings.TrimSpace(value)
					if strings.HasPrefix(value, `"`) {
						decoded, err := strconv.Unquote(value)
						if err != nil {
							return nil, fmt.Errorf("malformed Link relation")
						}
						value = decoded
					}
					relations = append(relations, strings.Fields(value)...)
				}
			}
			for _, rel := range relations {
				if rel != "next" {
					continue
				}
				if next != nil {
					return nil, fmt.Errorf("ambiguous next Link")
				}
				u, err := url.Parse(part[1:end])
				if err != nil {
					return nil, fmt.Errorf("invalid next Link")
				}
				u = current.ResolveReference(u)
				if u.Scheme != origin.Scheme || u.Host != origin.Host || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.Path != path {
					return nil, fmt.Errorf("pagination escaped requested resource")
				}
				query, err := url.ParseQuery(u.RawQuery)
				if err != nil {
					return nil, fmt.Errorf("invalid pagination query")
				}
				if len(query) != len(initial) {
					return nil, fmt.Errorf("pagination changed filters")
				}
				for key, values := range initial {
					got := query[key]
					if len(got) != 1 || len(values) != 1 {
						return nil, fmt.Errorf("ambiguous pagination filters")
					}
					if key != "page" && got[0] != values[0] {
						return nil, fmt.Errorf("pagination changed filters")
					}
				}
				page, err := strconv.Atoi(query.Get("page"))
				if err != nil || page < 1 {
					return nil, fmt.Errorf("invalid pagination page")
				}
				old, err := strconv.Atoi(current.Query().Get("page"))
				if err != nil || page != old+1 {
					return nil, fmt.Errorf("pagination skipped or repeated a page")
				}
				u.RawQuery = query.Encode()
				next = u
			}
		}
	}
	return next, nil
}

// Native APIs may add fields, so unknown API fields are allowed. Duplicate keys,
// malformed JSON, invalid UTF-8 and trailing values are not allowed to overwrite
// identity fields silently. Product record schemas remain strictly closed.
func decodeAPI(data []byte, out any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8 API response")
	}
	if err := validAPIUnicode(data); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := uniqueValue(d, 0); err != nil {
		return fmt.Errorf("invalid or ambiguous API JSON")
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing API JSON")
	}
	if err := exactAPIFieldNames(data, reflect.TypeOf(out)); err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected API response shape")
	}
	return nil
}

// encoding/json replaces lone surrogate escapes. Reject them instead of
// returning a rewritten body while claiming to preserve native exact content.
func validAPIUnicode(data []byte) error {
	inString := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inString = !inString
			continue
		}
		if !inString || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) || data[i] != 'u' {
			continue
		}
		if i+4 >= len(data) {
			return fmt.Errorf("invalid API Unicode escape")
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return fmt.Errorf("invalid API Unicode escape")
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return fmt.Errorf("unpaired API Unicode surrogate")
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return fmt.Errorf("unpaired API Unicode surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return fmt.Errorf("unpaired API Unicode surrogate")
			}
			i += 6
		}
	}
	return nil
}

// GitHub can add genuinely new API fields, but case-folded aliases of modeled
// fields are not new fields: encoding/json would otherwise overwrite identity.
func exactAPIFieldNames(data []byte, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return fmt.Errorf("unexpected API object shape")
		}
		for key, raw := range object {
			for i := 0; i < t.NumField(); i++ {
				field := t.Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name == "" {
					name = field.Name
				}
				if !strings.EqualFold(key, name) {
					continue
				}
				if key != name {
					return fmt.Errorf("case-aliased native API field")
				}
				if err := exactAPIFieldNames(raw, field.Type); err != nil {
					return err
				}
				break
			}
		}
	case reflect.Slice:
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return fmt.Errorf("unexpected API array shape")
		}
		for _, item := range items {
			if err := exactAPIFieldNames(item, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON too deep")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return ErrConflict
				}
				seen[s] = true
				if err := uniqueValue(d, depth+1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := uniqueValue(d, depth+1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected delimiter")
		}
		_, err = d.Token()
		return err
	}
	return nil
}
