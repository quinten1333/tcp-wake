// Package config loads and validates the tcp-wake configuration.
//
// The effective configuration is the TOML file overlaid with environment
// overrides, which win (ADR-0011). The file is located by --config, then
// $TCPWAKE_CONFIG, then the fixed default /etc/tcp-wake/config.toml, with no
// working-directory fallback (ADR-0015). A missing, unreadable, or malformed
// file prevents start with a message naming the offending key (ADR-0013).
package config

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultConfigPath is the fixed fallback location of the configuration file.
// It is also the mount point used by the container deployment (ADR-0015, T14).
const DefaultConfigPath = "/etc/tcp-wake/config.toml"

// envPrefix is prepended to the upper-cased key to form an environment
// override name, e.g. listen_address -> TCPWAKE_LISTEN_ADDRESS (ADR-0011).
const envPrefix = "TCPWAKE_"

// Config is the effective, validated configuration.
type Config struct {
	ListenAddress string
	TargetAddress string
	HealthPath    string
	ProbeInterval time.Duration
	ProbeTimeout  time.Duration
	WaitBound     time.Duration
	WakeCommand   string
	HeldBodyCap   ByteSize
}

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

// rawConfig holds the key set as the file and environment provide it: every
// value is a string because TOML cannot type a duration or a size (ADR-0013).
// The fields are prefilled with defaults, so a key absent from the file keeps
// its default and only present keys overwrite it.
type rawConfig struct {
	ListenAddress string `toml:"listen_address"`
	TargetAddress string `toml:"target_address"`
	HealthPath    string `toml:"health_path"`
	ProbeInterval string `toml:"probe_interval"`
	ProbeTimeout  string `toml:"probe_timeout"`
	WaitBound     string `toml:"wait_bound"`
	WakeCommand   string `toml:"wake_command"`
	HeldBodyCap   string `toml:"held_body_cap"`
}

// defaultRaw is the key set with the defaults shown in docs/config.example.toml.
func defaultRaw() rawConfig {
	return rawConfig{
		ListenAddress: "127.0.0.1:8080",
		TargetAddress: "http://hypha.lan:8080",
		HealthPath:    "/health",
		ProbeInterval: "2s",
		ProbeTimeout:  "1s",
		WaitBound:     "120s",
		WakeCommand:   "/usr/local/bin/wol-send",
		HeldBodyCap:   "64MiB",
	}
}

// keyField binds a configuration key to the raw string field it overrides.
type keyField struct {
	key string
	dst *string
}

// keyFields returns the key-to-field bindings for a given raw config. The order
// is stable so lookups and errors are deterministic.
func keyFields(raw *rawConfig) []keyField {
	return []keyField{
		{"listen_address", &raw.ListenAddress},
		{"target_address", &raw.TargetAddress},
		{"health_path", &raw.HealthPath},
		{"probe_interval", &raw.ProbeInterval},
		{"probe_timeout", &raw.ProbeTimeout},
		{"wait_bound", &raw.WaitBound},
		{"wake_command", &raw.WakeCommand},
		{"held_body_cap", &raw.HeldBodyCap},
	}
}

// envName maps a configuration key to its environment override name.
func envName(key string) string {
	return envPrefix + strings.ToUpper(key)
}

// ResolvePath determines the configuration file path from the command-line
// arguments and the environment, in the order --config, $TCPWAKE_CONFIG,
// DefaultConfigPath (ADR-0015). args must not include the program name.
func ResolvePath(args []string, lookupEnv func(string) (string, bool)) (string, error) {
	fs := flag.NewFlagSet("tcp-wake", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "path to the configuration file")
	if err := fs.Parse(args); err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	if *path != "" {
		return *path, nil
	}
	if lookupEnv != nil {
		if v, ok := lookupEnv(envPrefix + "CONFIG"); ok && v != "" {
			return v, nil
		}
	}
	return DefaultConfigPath, nil
}

// Load resolves, reads, parses, and validates the configuration. args must not
// include the program name; lookupEnv is the environment accessor (os.LookupEnv
// in production) and may be nil, which disables environment overrides.
func Load(args []string, lookupEnv func(string) (string, bool)) (*Config, error) {
	path, err := ResolvePath(args, lookupEnv)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: cannot read %s: %w", path, err)
	}

	raw := defaultRaw()
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, fmt.Errorf("config: %s: malformed TOML: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("config: %s: unknown key %q", path, undecoded[0].String())
	}

	applyEnv(&raw, lookupEnv)

	return parse(&raw)
}

// applyEnv overlays environment overrides onto raw. A variable that is present
// in the environment always wins over the file, including an empty value, which
// validation later rejects rather than silently falling back (ADR-0011).
func applyEnv(raw *rawConfig, lookupEnv func(string) (string, bool)) {
	if lookupEnv == nil {
		return
	}
	for _, f := range keyFields(raw) {
		if v, ok := lookupEnv(envName(f.key)); ok {
			*f.dst = v
		}
	}
}

// parse converts the raw strings into typed, validated values, naming the
// offending key in every error.
func parse(raw *rawConfig) (*Config, error) {
	for _, f := range keyFields(raw) {
		if strings.TrimSpace(*f.dst) == "" {
			return nil, fmt.Errorf("config: %s: must not be empty", f.key)
		}
	}

	probeInterval, err := parseDuration("probe_interval", raw.ProbeInterval)
	if err != nil {
		return nil, err
	}
	probeTimeout, err := parseDuration("probe_timeout", raw.ProbeTimeout)
	if err != nil {
		return nil, err
	}
	waitBound, err := parseDuration("wait_bound", raw.WaitBound)
	if err != nil {
		return nil, err
	}
	heldBodyCap, err := parseSize("held_body_cap", raw.HeldBodyCap)
	if err != nil {
		return nil, err
	}

	return &Config{
		ListenAddress: raw.ListenAddress,
		TargetAddress: raw.TargetAddress,
		HealthPath:    raw.HealthPath,
		ProbeInterval: probeInterval,
		ProbeTimeout:  probeTimeout,
		WaitBound:     waitBound,
		WakeCommand:   raw.WakeCommand,
		HeldBodyCap:   heldBodyCap,
	}, nil
}

func parseDuration(key, val string) (time.Duration, error) {
	d, err := time.ParseDuration(val)
	if err != nil {
		return 0, fmt.Errorf("config: %s: invalid duration %q", key, val)
	}
	if d <= 0 {
		return 0, fmt.Errorf("config: %s: duration must be positive, got %q", key, val)
	}
	return d, nil
}

func parseSize(key, val string) (ByteSize, error) {
	b, err := ParseByteSize(val)
	if err != nil {
		return 0, fmt.Errorf("config: %s: %v", key, err)
	}
	return b, nil
}
