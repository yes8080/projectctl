package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxRecordBytes = 256 * 1024
	maxDepth       = 64
)

// SHA256 hashes exact bytes, without trimming or adding a newline.
func SHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// CanonicalJSON implements the protocol's integer-only dialect, not RFC 8785:
// UTF-8 key ordering, compact output, no Unicode normalization, array order
// preserved, integers in signed 64-bit decimal notation. Fractions, exponents,
// -0, duplicate keys and invalid Unicode are rejected.
func CanonicalJSON(data []byte) ([]byte, error) {
	v, err := parseJSON(data)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	appendCanonical(&out, v)
	return out.Bytes(), nil
}

func DigestJSON(data []byte) (string, error) {
	canonical, err := CanonicalJSON(data)
	if err != nil {
		return "", err
	}
	return SHA256(canonical), nil
}

func parseJSON(data []byte) (any, error) {
	if len(data) == 0 || len(data) > MaxRecordBytes {
		return nil, fmt.Errorf("JSON size must be 1..%d bytes", MaxRecordBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("invalid UTF-8")
	}
	if err := validateSurrogates(data); err != nil {
		return nil, err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := readValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing JSON content")
	}
	return v, nil
}

func readValue(d *json.Decoder, depth int) (any, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("JSON nesting exceeds %d", maxDepth)
	}
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := make(map[string]any)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return nil, err
				}
				k, ok := key.(string)
				if !ok {
					return nil, fmt.Errorf("object key must be string")
				}
				if _, exists := m[k]; exists {
					return nil, fmt.Errorf("duplicate field %q", k)
				}
				v, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				m[k] = v
			}
			_, err := d.Token()
			return m, err
		case '[':
			a := make([]any, 0)
			for d.More() {
				v, err := readValue(d, depth+1)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token()
			return a, err
		default:
			return nil, fmt.Errorf("unexpected delimiter")
		}
	case json.Number:
		s := t.String()
		if strings.ContainsAny(s, ".eE") || s == "-0" {
			return nil, fmt.Errorf("non-canonical integer %q", s)
		}
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return nil, fmt.Errorf("integer outside int64 range: %w", err)
		}
		return t, nil
	case nil, bool, string:
		return t, nil
	default:
		return nil, fmt.Errorf("unsupported JSON token")
	}
}

// encoding/json replaces lone UTF-16 surrogates with U+FFFD. Reject them before
// decoding so distinct inputs cannot silently become the same fact.
func validateSurrogates(data []byte) error {
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
			return fmt.Errorf("short Unicode escape")
		}
		n, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return fmt.Errorf("invalid Unicode escape")
		}
		i += 4
		if n >= 0xDC00 && n <= 0xDFFF {
			return fmt.Errorf("unpaired low surrogate")
		}
		if n >= 0xD800 && n <= 0xDBFF {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return fmt.Errorf("unpaired high surrogate")
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return fmt.Errorf("unpaired high surrogate")
			}
			i += 6
		}
	}
	return nil
}

func appendCanonical(out *bytes.Buffer, v any) {
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteByte('{')
		for i, k := range keys {
			if i != 0 {
				out.WriteByte(',')
			}
			appendString(out, k)
			out.WriteByte(':')
			appendCanonical(out, v[k])
		}
		out.WriteByte('}')
	case []any:
		out.WriteByte('[')
		for i, item := range v {
			if i != 0 {
				out.WriteByte(',')
			}
			appendCanonical(out, item)
		}
		out.WriteByte(']')
	case string:
		appendString(out, v)
	case json.Number:
		out.WriteString(v.String())
	case bool:
		out.WriteString(strconv.FormatBool(v))
	case nil:
		out.WriteString("null")
	}
}

func appendString(out *bytes.Buffer, s string) {
	out.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(out, `\u%04x`, r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
}
