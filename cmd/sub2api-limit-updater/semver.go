package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

type semVersion struct {
	major uint64
	minor uint64
	patch uint64
	pre   []string
}

func parseSemVersion(raw string) (semVersion, error) {
	matches := semverPattern.FindStringSubmatch(raw)
	if matches == nil {
		return semVersion{}, fmt.Errorf("invalid semantic version %q", raw)
	}
	numbers := make([]uint64, 3)
	for i := range numbers {
		value, err := strconv.ParseUint(matches[i+1], 10, 64)
		if err != nil {
			return semVersion{}, fmt.Errorf("invalid semantic version %q", raw)
		}
		numbers[i] = value
	}
	var pre []string
	if matches[4] != "" {
		pre = strings.Split(matches[4], ".")
		for _, identifier := range pre {
			if isNumericIdentifier(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return semVersion{}, fmt.Errorf("invalid semantic version %q", raw)
			}
		}
	}
	return semVersion{major: numbers[0], minor: numbers[1], patch: numbers[2], pre: pre}, nil
}

func compareSemVersion(left, right semVersion) int {
	if comparison := compareUint(left.major, right.major); comparison != 0 {
		return comparison
	}
	if comparison := compareUint(left.minor, right.minor); comparison != 0 {
		return comparison
	}
	if comparison := compareUint(left.patch, right.patch); comparison != 0 {
		return comparison
	}
	if len(left.pre) == 0 && len(right.pre) == 0 {
		return 0
	}
	if len(left.pre) == 0 {
		return 1
	}
	if len(right.pre) == 0 {
		return -1
	}
	for i := 0; i < len(left.pre) && i < len(right.pre); i++ {
		if left.pre[i] == right.pre[i] {
			continue
		}
		leftNumeric := isNumericIdentifier(left.pre[i])
		rightNumeric := isNumericIdentifier(right.pre[i])
		switch {
		case leftNumeric && rightNumeric:
			return compareNumericIdentifier(left.pre[i], right.pre[i])
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case left.pre[i] < right.pre[i]:
			return -1
		default:
			return 1
		}
	}
	return compareUint(uint64(len(left.pre)), uint64(len(right.pre)))
}

func compareUint(left, right uint64) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
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

func compareNumericIdentifier(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	return 1
}
