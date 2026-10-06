// This file owns the ByteSize value type: a byte count parsed from an
// IEC-suffixed string, with the parsing and rendering kept out of the loader.
package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ByteSize is a count of bytes parsed from an IEC-suffixed string such as
// "64MiB". TOML has no native size type, so sizes are validated at start.
type ByteSize int64

const (
	kib = 1024
	mib = 1024 * kib
	gib = 1024 * mib
	tib = 1024 * gib
)

// ParseByteSize parses a decimal number with an optional IEC suffix: "B",
// "KiB", "MiB", "GiB", or "TiB". No suffix means bytes. The value must be
// positive; empty input, an unknown suffix, or trailing garbage is an error.
func ParseByteSize(s string) (ByteSize, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty size")
	}

	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q", orig)
	}

	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", orig)
	}

	var mult int64
	switch s[i:] {
	case "", "B", "b":
		mult = 1
	case "KiB":
		mult = kib
	case "MiB":
		mult = mib
	case "GiB":
		mult = gib
	case "TiB":
		mult = tib
	default:
		return 0, fmt.Errorf("invalid size %q: unknown suffix %q", orig, s[i:])
	}

	if n <= 0 {
		return 0, fmt.Errorf("invalid size %q: must be positive", orig)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q: overflows int64", orig)
	}
	return ByteSize(n * mult), nil
}

// String renders the size with an IEC suffix, rounded to one decimal place.
func (b ByteSize) String() string {
	if b < kib {
		return fmt.Sprintf("%dB", int64(b))
	}
	div, exp := int64(kib), 0
	for n := int64(b) / kib; n >= kib; n /= kib {
		div *= kib
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
