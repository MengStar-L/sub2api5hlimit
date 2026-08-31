package sub2api

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version reported by Sub2API.
type Version struct {
	Raw        string `json:"raw"`
	Major      int    `json:"major"`
	Minor      int    `json:"minor"`
	Patch      int    `json:"patch"`
	PreRelease string `json:"pre_release,omitempty"`
}

func ParseVersion(value string) (Version, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return Version{}, fmt.Errorf("sub2api version is empty")
	}

	core := raw
	if core[0] == 'v' || core[0] == 'V' {
		core = core[1:]
	}
	if buildIndex := strings.IndexByte(core, '+'); buildIndex >= 0 {
		if err := validateIdentifiers(core[buildIndex+1:], false); err != nil {
			return Version{}, fmt.Errorf("invalid sub2api version %q", raw)
		}
		core = core[:buildIndex]
	}

	preRelease := ""
	if preIndex := strings.IndexByte(core, '-'); preIndex >= 0 {
		preRelease = core[preIndex+1:]
		core = core[:preIndex]
		if preRelease == "" {
			return Version{}, fmt.Errorf("invalid sub2api version %q", raw)
		}
		if err := validateIdentifiers(preRelease, true); err != nil {
			return Version{}, fmt.Errorf("invalid sub2api version %q", raw)
		}
	}

	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("invalid sub2api version %q", raw)
	}
	values := [3]int{}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Version{}, fmt.Errorf("invalid sub2api version %q", raw)
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return Version{}, fmt.Errorf("invalid sub2api version %q", raw)
		}
		values[i] = value
	}

	return Version{
		Raw:        raw,
		Major:      values[0],
		Minor:      values[1],
		Patch:      values[2],
		PreRelease: preRelease,
	}, nil
}

func (v Version) String() string {
	if v.Raw != "" {
		return v.Raw
	}
	value := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.PreRelease != "" {
		value += "-" + v.PreRelease
	}
	return value
}

// AtLeast compares semantic versions. Build metadata is ignored and a
// prerelease is lower than the corresponding release.
func (v Version) AtLeast(minimum Version) bool {
	left := [...]int{v.Major, v.Minor, v.Patch}
	right := [...]int{minimum.Major, minimum.Minor, minimum.Patch}
	for i := range left {
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	return comparePrerelease(v.PreRelease, minimum.PreRelease) >= 0
}

func comparePrerelease(left, right string) int {
	if left == right {
		return 0
	}
	if left == "" {
		return 1
	}
	if right == "" {
		return -1
	}

	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for i := 0; i < len(leftParts) && i < len(rightParts); i++ {
		if leftParts[i] == rightParts[i] {
			continue
		}
		leftNumeric := isNumericIdentifier(leftParts[i])
		rightNumeric := isNumericIdentifier(rightParts[i])
		switch {
		case leftNumeric && rightNumeric:
			if len(leftParts[i]) != len(rightParts[i]) {
				if len(leftParts[i]) < len(rightParts[i]) {
					return -1
				}
				return 1
			}
			if leftParts[i] < rightParts[i] {
				return -1
			}
			return 1
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case leftParts[i] < rightParts[i]:
			return -1
		default:
			return 1
		}
	}
	if len(leftParts) < len(rightParts) {
		return -1
	}
	return 1
}

func validateIdentifiers(value string, rejectNumericLeadingZeros bool) error {
	if value == "" {
		return errors.New("empty identifier")
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return errors.New("empty identifier")
		}
		for _, character := range identifier {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return errors.New("invalid identifier character")
		}
		if rejectNumericLeadingZeros && len(identifier) > 1 && identifier[0] == '0' && isNumericIdentifier(identifier) {
			return errors.New("numeric identifier has a leading zero")
		}
	}
	return nil
}

func isNumericIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
