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
	"os"
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
	// WakeMAC is the target's MAC address, required (ADR-0016).
	WakeMAC string
	// WakeInterface is the interface etherwake sends on. Empty means omit -i
	// and let etherwake choose its own default (ADR-0016).
	WakeInterface string
	HeldBodyCap   ByteSize
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
	WakeMAC       string `toml:"wake_mac"`
	WakeInterface string `toml:"wake_interface"`
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
		WakeMAC:       "", // required; no default
		WakeInterface: "", // empty: omit -i and use etherwake's default
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
		{"wake_mac", &raw.WakeMAC},
		{"wake_interface", &raw.WakeInterface},
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
		// wake_interface is optional: empty means omit -i and let etherwake
		// choose its own default (ADR-0016).
		if f.key == "wake_interface" {
			continue
		}
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
		WakeMAC:       raw.WakeMAC,
		WakeInterface: raw.WakeInterface,
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
