package greeting

import "testing"

func TestGreet(t *testing.T) {
	cases := []struct{ name, want string }{
		{"Ada", "Hello, Ada!"},
		{"  Ada  ", "Hello, Ada!"},
		{"", "Hello, world!"},
		{" \t\n", "Hello, world!"},
		{"世界", "Hello, 世界!"},
	}
	for _, c := range cases {
		if got := Greet(c.name); got != c.want {
			t.Errorf("Greet(%q)=%q, want %q", c.name, got, c.want)
		}
	}
}
