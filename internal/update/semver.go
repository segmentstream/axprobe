package update

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	major int
	minor int
	patch int
	pre   string
}

// compareVersions returns -1, 0, or 1 for left vs right (semver, pre-release
// aware). A non-MAJOR.MINOR.PATCH input (e.g. "dev") returns an error, which is
// how a dev build is barred from self-updating.
func compareVersions(left, right string) (int, error) {
	a, err := parseVersion(left)
	if err != nil {
		return 0, err
	}
	b, err := parseVersion(right)
	if err != nil {
		return 0, err
	}

	for _, pair := range [][2]int{
		{a.major, b.major},
		{a.minor, b.minor},
		{a.patch, b.patch},
	} {
		if pair[0] < pair[1] {
			return -1, nil
		}
		if pair[0] > pair[1] {
			return 1, nil
		}
	}

	if a.pre == b.pre {
		return 0, nil
	}
	if a.pre == "" {
		return 1, nil
	}
	if b.pre == "" {
		return -1, nil
	}
	if a.pre < b.pre {
		return -1, nil
	}
	return 1, nil
}

func normalizeVersion(input string) string {
	return strings.TrimPrefix(strings.TrimSpace(input), "v")
}

func parseVersion(input string) (semanticVersion, error) {
	version := normalizeVersion(input)
	base, pre, _ := strings.Cut(version, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return semanticVersion{}, fmt.Errorf("version %q is not in MAJOR.MINOR.PATCH format", input)
	}

	nums := make([]int, 3)
	for i, part := range parts {
		if part == "" {
			return semanticVersion{}, fmt.Errorf("version %q is not in MAJOR.MINOR.PATCH format", input)
		}
		num, err := strconv.Atoi(part)
		if err != nil {
			return semanticVersion{}, fmt.Errorf("version %q is not in MAJOR.MINOR.PATCH format", input)
		}
		nums[i] = num
	}

	return semanticVersion{major: nums[0], minor: nums[1], patch: nums[2], pre: pre}, nil
}
