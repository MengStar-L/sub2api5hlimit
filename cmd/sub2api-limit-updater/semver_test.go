package main

import "testing"

func TestSemanticVersionOrdering(t *testing.T) {
	t.Parallel()
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{"0.2.0", "0.1.9", 1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.2.3+first", "1.2.3+second", 0},
	}
	for _, test := range tests {
		left, err := parseSemVersion(test.left)
		if err != nil {
			t.Fatalf("parse %s: %v", test.left, err)
		}
		right, err := parseSemVersion(test.right)
		if err != nil {
			t.Fatalf("parse %s: %v", test.right, err)
		}
		if got := compareSemVersion(left, right); got != test.want {
			t.Fatalf("compare %s to %s = %d, want %d", test.left, test.right, got, test.want)
		}
	}
}

func TestSemanticVersionRejectsLeadingZero(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"01.2.3", "1.02.3", "1.2.03", "1.2.3-01"} {
		if _, err := parseSemVersion(value); err == nil {
			t.Fatalf("parseSemVersion(%q) unexpectedly succeeded", value)
		}
	}
}
