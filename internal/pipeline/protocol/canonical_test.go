package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestCanonicalGoldens(t *testing.T) {
	data, err := os.ReadFile("testdata/canonical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name      string
		Input     string
		Canonical string
		SHA256    string
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			got, err := CanonicalJSON([]byte(v.Input))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != v.Canonical {
				t.Fatalf("got %q; want %q", got, v.Canonical)
			}
			if hash := SHA256(got); hash != v.SHA256 {
				t.Fatalf("digest %s; want %s", hash, v.SHA256)
			}
			again, err := CanonicalJSON(got)
			if err != nil || !bytes.Equal(got, again) {
				t.Fatalf("not idempotent: %v", err)
			}
		})
	}
}

func TestCanonicalRejectsAmbiguity(t *testing.T) {
	for name, input := range map[string]string{
		"duplicate":        `{"a":1,"a":2}`,
		"nested duplicate": `{"x":[{"a":1,"\u0061":2}]}`,
		"trailing":         `{} []`, "empty": "", "incomplete": `{"a":`,
		"overflow": `9223372036854775808`, "underflow": `-9223372036854775809`,
		"float": `1.0`, "exponent": `1e0`, "negative zero": `-0`,
		"high surrogate": `"\ud800"`, "low surrogate": `"\udc00"`,
		"bad pair": `"\ud800\u1234"`, "short escape": `"\ud8"`,
		"invalid UTF8": string([]byte{'"', 0xff, '"'}),
		"oversize":     strings.Repeat(" ", MaxRecordBytes+1),
		"depth":        strings.Repeat("[", maxDepth+2) + "0" + strings.Repeat("]", maxDepth+2),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalJSON([]byte(input)); err == nil {
				t.Fatal("ambiguous input accepted")
			}
		})
	}
}

func TestCanonicalSemanticBoundaries(t *testing.T) {
	a, err := DigestJSON([]byte(`{ "b":2,"a":"\u0061" }`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := DigestJSON([]byte(`{"a":"a","b":2}`))
	if err != nil || a != b {
		t.Fatal("order/escape changed digest")
	}
	for _, pair := range [][2]string{{`[1,2]`, `[2,1]`}, {`{}`, `{"a":null}`}, {`"é"`, `"é"`}, {`1`, `2`}} {
		x, _ := DigestJSON([]byte(pair[0]))
		y, _ := DigestJSON([]byte(pair[1]))
		if x == y {
			t.Fatal("distinct values collapsed")
		}
	}
	if SHA256([]byte("x")) == SHA256([]byte("x\n")) {
		t.Fatal("raw body newline lost")
	}
}

func FuzzCanonicalJSON(f *testing.F) {
	for _, seed := range []string{`{"z":1,"a":[true,null,"😀"]}`, `{"a":1,"a":2}`, `"\ud800"`, `-0`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		out, err := CanonicalJSON([]byte(input))
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("invalid canonical output: %q", out)
		}
		again, err := CanonicalJSON(out)
		if err != nil || !bytes.Equal(out, again) {
			t.Fatal("not idempotent")
		}
	})
}
