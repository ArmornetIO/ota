package ota

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed semantic version (semver 2.0). Build metadata is kept for
// display but ignored in comparisons, as the spec requires.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
	raw                 string
}

// ParseVersion accepts "1.8.0", "v1.8.0", "1.9.0-rc.1+abc". A leading "v" is
// allowed because git tags carry one and offers are generated from tags.
func ParseVersion(s string) (Version, error) {
	raw := s
	s = strings.TrimPrefix(s, "v")
	var v Version
	v.raw = raw
	if i := strings.IndexByte(s, '+'); i >= 0 {
		v.Build = s[i+1:]
		s = s[:i]
		if v.Build == "" {
			return Version{}, fmt.Errorf("ota: version %q has empty build metadata", raw)
		}
	}
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre := s[i+1:]
		s = s[:i]
		if pre == "" {
			return Version{}, fmt.Errorf("ota: version %q has an empty prerelease", raw)
		}
		v.Pre = strings.Split(pre, ".")
		for _, id := range v.Pre {
			if id == "" || !validIdent(id) {
				return Version{}, fmt.Errorf("ota: version %q has an invalid prerelease identifier %q", raw, id)
			}
			if isNumeric(id) && len(id) > 1 && id[0] == '0' {
				return Version{}, fmt.Errorf("ota: version %q has a numeric prerelease identifier with a leading zero", raw)
			}
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("ota: version %q is not MAJOR.MINOR.PATCH", raw)
	}
	nums := make([]uint64, 3)
	for i, p := range parts {
		if p == "" || !isNumeric(p) || (len(p) > 1 && p[0] == '0') {
			return Version{}, fmt.Errorf("ota: version %q has an invalid number %q", raw, p)
		}
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("ota: version %q: %w", raw, err)
		}
		nums[i] = n
	}
	v.Major, v.Minor, v.Patch = nums[0], nums[1], nums[2]
	return v, nil
}

// MustParseVersion is for constants in tests and examples.
func MustParseVersion(s string) Version {
	v, err := ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return v
}

// String renders the version without a leading "v", the form offers use.
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare returns -1, 0 or 1. A prerelease sorts before its release, which is
// what keeps 1.9.0-rc.1 from being offered as an upgrade over 1.9.0.
func (v Version) Compare(o Version) int {
	for _, d := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(v.Pre) == 0 && len(o.Pre) == 0:
		return 0
	case len(v.Pre) == 0:
		return 1
	case len(o.Pre) == 0:
		return -1
	}
	for i := 0; i < len(v.Pre) && i < len(o.Pre); i++ {
		if c := compareIdent(v.Pre[i], o.Pre[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(v.Pre) < len(o.Pre):
		return -1
	case len(v.Pre) > len(o.Pre):
		return 1
	}
	return 0
}

func compareIdent(a, b string) int {
	an, bn := isNumeric(a), isNumeric(b)
	switch {
	case an && bn:
		x, _ := strconv.ParseUint(a, 10, 64)
		y, _ := strconv.ParseUint(b, 10, 64)
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func validIdent(s string) bool {
	for _, r := range s {
		if !(r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}
