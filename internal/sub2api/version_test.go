package sub2api

import "testing"

func TestVersionComparison(t *testing.T) {
	t.Parallel()

	minimum, err := ParseVersion("0.1.183")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		version string
		want    bool
	}{
		{version: "v0.1.182", want: false},
		{version: "0.1.183-rc.1", want: false},
		{version: "0.1.183", want: true},
		{version: "0.1.183+build.7", want: true},
		{version: "0.1.184", want: true},
		{version: "0.2.0", want: true},
		{version: "1.0.0", want: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.version, func(t *testing.T) {
			t.Parallel()
			version, err := ParseVersion(test.version)
			if err != nil {
				t.Fatalf("ParseVersion() error = %v", err)
			}
			if got := version.AtLeast(minimum); got != test.want {
				t.Fatalf("AtLeast() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestParseVersionRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"", "dev", "1.2", "1.2.3.4", "01.2.3", "1.02.3", "1.2.03", "1.2.x",
		"1.2.3-", "1.2.3+", "1.2.3-alpha..1", "1.2.3-alpha!", "1.2.3-01",
	} {
		if _, err := ParseVersion(value); err == nil {
			t.Errorf("ParseVersion(%q) unexpectedly succeeded", value)
		}
	}
}
