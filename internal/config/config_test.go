package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

func nopEnv(string) (string, bool) { return "", false }

func envFrom(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func loadContent(t *testing.T, content string, env map[string]string) (*Config, error) {
	t.Helper()
	e := nopEnv
	if env != nil {
		e = envFrom(env)
	}
	return Load([]string{"--config", writeConfig(t, content)}, e)
}

func mustLoad(t *testing.T, content string, env map[string]string) *Config {
	t.Helper()
	cfg, err := loadContent(t, content, env)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	return cfg
}

func TestLoadDefaults(t *testing.T) {
	cfg := mustLoad(t, "# no keys\n", nil)

	if cfg.ListenAddress != "127.0.0.1:8080" {
		t.Errorf("ListenAddress = %q", cfg.ListenAddress)
	}
	if cfg.TargetAddress != "http://hypha.lan:8080" {
		t.Errorf("TargetAddress = %q", cfg.TargetAddress)
	}
	if cfg.HealthPath != "/health" {
		t.Errorf("HealthPath = %q", cfg.HealthPath)
	}
	if cfg.ProbeInterval != 2*time.Second {
		t.Errorf("ProbeInterval = %v", cfg.ProbeInterval)
	}
	if cfg.ProbeTimeout != time.Second {
		t.Errorf("ProbeTimeout = %v", cfg.ProbeTimeout)
	}
	if cfg.WaitBound != 120*time.Second {
		t.Errorf("WaitBound = %v", cfg.WaitBound)
	}
	if cfg.WakeCommand != "/usr/local/bin/wol-send" {
		t.Errorf("WakeCommand = %q", cfg.WakeCommand)
	}
	if cfg.HeldBodyCap != ByteSize(64*1024*1024) {
		t.Errorf("HeldBodyCap = %d", cfg.HeldBodyCap)
	}
}

// TestDefaultsMatchExampleFile pins the code defaults to the values documented
// in the example files, and fails if a key is added to one but not the other
// (which also checks the toml tags match the files' key set). Both the
// annotated docs example and the small root example must agree with the code.
func TestDefaultsMatchExampleFile(t *testing.T) {
	for _, path := range []string{
		filepath.Join("..", "..", "docs", "config.example.toml"),
		filepath.Join("..", "..", "config.toml.example"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}

			var fromFile rawConfig
			md, err := toml.Decode(string(data), &fromFile)
			if err != nil {
				t.Fatalf("decode example file: %v", err)
			}
			if und := md.Undecoded(); len(und) > 0 {
				t.Fatalf("example file has keys absent from rawConfig: %v", und)
			}

			if want := defaultRaw(); fromFile != want {
				t.Errorf("example file values = %+v\ncode defaults      = %+v", fromFile, want)
			}
		})
	}
}

func TestLoadEachKeyFromFile(t *testing.T) {
	cases := []struct {
		name  string
		toml  string
		check func(*testing.T, *Config)
	}{
		{
			"listen_address",
			`listen_address = "0.0.0.0:9090"`,
			func(t *testing.T, c *Config) {
				if c.ListenAddress != "0.0.0.0:9090" {
					t.Errorf("ListenAddress = %q", c.ListenAddress)
				}
			},
		},
		{
			"target_address",
			`target_address = "http://other.lan:1234"`,
			func(t *testing.T, c *Config) {
				if c.TargetAddress != "http://other.lan:1234" {
					t.Errorf("TargetAddress = %q", c.TargetAddress)
				}
			},
		},
		{
			"health_path",
			`health_path = "/ready"`,
			func(t *testing.T, c *Config) {
				if c.HealthPath != "/ready" {
					t.Errorf("HealthPath = %q", c.HealthPath)
				}
			},
		},
		{
			"probe_interval",
			`probe_interval = "500ms"`,
			func(t *testing.T, c *Config) {
				if c.ProbeInterval != 500*time.Millisecond {
					t.Errorf("ProbeInterval = %v", c.ProbeInterval)
				}
			},
		},
		{
			"probe_timeout",
			`probe_timeout = "250ms"`,
			func(t *testing.T, c *Config) {
				if c.ProbeTimeout != 250*time.Millisecond {
					t.Errorf("ProbeTimeout = %v", c.ProbeTimeout)
				}
			},
		},
		{
			"wait_bound",
			`wait_bound = "30s"`,
			func(t *testing.T, c *Config) {
				if c.WaitBound != 30*time.Second {
					t.Errorf("WaitBound = %v", c.WaitBound)
				}
			},
		},
		{
			"wake_command",
			`wake_command = "/usr/local/bin/other"`,
			func(t *testing.T, c *Config) {
				if c.WakeCommand != "/usr/local/bin/other" {
					t.Errorf("WakeCommand = %q", c.WakeCommand)
				}
			},
		},
		{
			"held_body_cap",
			`held_body_cap = "1GiB"`,
			func(t *testing.T, c *Config) {
				if c.HeldBodyCap != ByteSize(1024*1024*1024) {
					t.Errorf("HeldBodyCap = %d", c.HeldBodyCap)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, mustLoad(t, tc.toml+"\n", nil))
		})
	}
}

func TestEnvOverrideWinsOverFile(t *testing.T) {
	cfg := mustLoad(t,
		`listen_address = "1.2.3.4:1"`+"\n",
		map[string]string{"TCPWAKE_LISTEN_ADDRESS": "9.9.9.9:9"},
	)
	if cfg.ListenAddress != "9.9.9.9:9" {
		t.Errorf("ListenAddress = %q, want environment value", cfg.ListenAddress)
	}
}

func TestEnvOverrideAppliesToAbsentKey(t *testing.T) {
	cfg := mustLoad(t, "", map[string]string{"TCPWAKE_WAIT_BOUND": "5s"})
	if cfg.WaitBound != 5*time.Second {
		t.Errorf("WaitBound = %v, want 5s from environment", cfg.WaitBound)
	}
}

func TestEnvOverrideEmptyIsAnError(t *testing.T) {
	_, err := loadContent(t, "", map[string]string{"TCPWAKE_HEALTH_PATH": ""})
	if err == nil {
		t.Fatal("expected an error for an empty environment override")
	}
	if !strings.Contains(err.Error(), "health_path") {
		t.Errorf("error %q does not name the key", err)
	}
}

func TestMalformedValues(t *testing.T) {
	cases := []struct {
		name string
		toml string
		env  map[string]string
		key  string
	}{
		{"duration_file", `probe_interval = "2x"`, nil, "probe_interval"},
		{"duration_file_wait", `wait_bound = "fast"`, nil, "wait_bound"},
		{"size_file", `held_body_cap = "64MB"`, nil, "held_body_cap"},
		{"duration_env", "", map[string]string{"TCPWAKE_WAIT_BOUND": "fast"}, "wait_bound"},
		{"size_env", "", map[string]string{"TCPWAKE_HELD_BODY_CAP": "64MB"}, "held_body_cap"},
		{"zero_duration", `probe_timeout = "0s"`, nil, "probe_timeout"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadContent(t, tc.toml, tc.env)
			if err == nil {
				t.Fatalf("expected an error naming %q", tc.key)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("error %q does not name key %q", err, tc.key)
			}
		})
	}
}

func TestUnknownKeyFails(t *testing.T) {
	_, err := loadContent(t, `listen_addr = "127.0.0.1:8080"`+"\n", nil)
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if !strings.Contains(err.Error(), "listen_addr") {
		t.Errorf("error %q does not name the unknown key", err)
	}
}

func TestSyntaxErrorFails(t *testing.T) {
	_, err := loadContent(t, "listen_address =\n", nil)
	if err == nil {
		t.Fatal("expected an error for malformed TOML")
	}
	if !strings.Contains(err.Error(), "malformed TOML") {
		t.Errorf("error %q does not mention malformed TOML", err)
	}
}

func TestMissingFileNamesPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")
	_, err := Load([]string{"--config", missing}, nopEnv)
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the path %q", err, missing)
	}
}

func TestResolvePath(t *testing.T) {
	t.Run("flag_wins", func(t *testing.T) {
		got, err := ResolvePath([]string{"--config", "/a.toml"}, envFrom(map[string]string{"TCPWAKE_CONFIG": "/b.toml"}))
		if err != nil {
			t.Fatal(err)
		}
		if got != "/a.toml" {
			t.Errorf("ResolvePath = %q, want /a.toml", got)
		}
	})

	t.Run("env_wins", func(t *testing.T) {
		got, err := ResolvePath(nil, envFrom(map[string]string{"TCPWAKE_CONFIG": "/b.toml"}))
		if err != nil {
			t.Fatal(err)
		}
		if got != "/b.toml" {
			t.Errorf("ResolvePath = %q, want /b.toml", got)
		}
	})

	t.Run("default", func(t *testing.T) {
		got, err := ResolvePath(nil, nopEnv)
		if err != nil {
			t.Fatal(err)
		}
		if got != DefaultConfigPath {
			t.Errorf("ResolvePath = %q, want %q", got, DefaultConfigPath)
		}
	})

	t.Run("empty_env_falls_through", func(t *testing.T) {
		got, err := ResolvePath(nil, envFrom(map[string]string{"TCPWAKE_CONFIG": ""}))
		if err != nil {
			t.Fatal(err)
		}
		if got != DefaultConfigPath {
			t.Errorf("ResolvePath = %q, want %q", got, DefaultConfigPath)
		}
	})

	t.Run("bad_flag", func(t *testing.T) {
		if _, err := ResolvePath([]string{"--bogus"}, nopEnv); err == nil {
			t.Fatal("expected an error for an unknown flag")
		}
	})
}

func TestParseByteSize(t *testing.T) {
	valid := []struct {
		in   string
		want ByteSize
	}{
		{"1B", 1},
		{"512", 512},
		{"1KiB", 1024},
		{"2KiB", 2048},
		{"64MiB", 64 * 1024 * 1024},
		{"1GiB", 1024 * 1024 * 1024},
		{"1TiB", 1024 * 1024 * 1024 * 1024},
		{" 1MiB ", 1024 * 1024},
	}
	for _, tc := range valid {
		got, err := ParseByteSize(tc.in)
		if err != nil {
			t.Errorf("ParseByteSize(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		"", "0", "-1MiB", "64MB", "abc", "1.5MiB", "MiB",
		"99999999999999999999TiB", "1 KiB ", "1_000",
	}
	for _, in := range invalid {
		if got, err := ParseByteSize(in); err == nil {
			t.Errorf("ParseByteSize(%q) = %d, want error", in, got)
		}
	}
}

func TestByteSizeString(t *testing.T) {
	cases := []struct {
		in   ByteSize
		want string
	}{
		{0, "0B"},
		{1024, "1.0KiB"},
		{64 * 1024 * 1024, "64.0MiB"},
	}
	for _, tc := range cases {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("ByteSize(%d).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRawConfigTagsMatchExampleFile is a reflection-based guard: every toml tag
// on rawConfig is a key the example file actually documents.
func TestRawConfigTagsMatchExampleFile(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]any
	if _, err := toml.Decode(string(data), &file); err != nil {
		t.Fatal(err)
	}

	typ := reflect.TypeOf(rawConfig{})
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("toml")
		if tag == "" {
			t.Errorf("field %s has no toml tag", typ.Field(i).Name)
			continue
		}
		if _, ok := file[tag]; !ok {
			t.Errorf("rawConfig tag %q is not a key in the example file", tag)
		}
	}
}

// TestIF5EveryKeyComesFromTheFile is the acceptance test for IF-5: the full key
// set is read from the configuration file. It is deliberately separate from the
// per-key table so the RTM can point one check at one test.
func TestIF5EveryKeyComesFromTheFile(t *testing.T) {
	cfg := mustLoad(t, `
listen_address = "10.0.0.1:1234"
target_address = "http://t.lan:4321"
health_path    = "/readyz"
probe_interval = "3s"
probe_timeout  = "2s"
wait_bound     = "9s"
wake_command   = "/bin/wake"
held_body_cap  = "1KiB"
`, nil)

	if cfg.ListenAddress != "10.0.0.1:1234" ||
		cfg.TargetAddress != "http://t.lan:4321" ||
		cfg.HealthPath != "/readyz" ||
		cfg.ProbeInterval != 3*time.Second ||
		cfg.ProbeTimeout != 2*time.Second ||
		cfg.WaitBound != 9*time.Second ||
		cfg.WakeCommand != "/bin/wake" ||
		cfg.HeldBodyCap != ByteSize(1024) {
		t.Fatalf("the file's keys were not all applied: %+v", cfg)
	}
}

// TestNFR1DefaultWaitBoundCoversBoot ties NFR-1 to the configured default: the
// default 120 s bound must exceed hypha's ~20 s boot (C-2) and the worst-case
// probe detection (interval + timeout), leaving margin. The runtime shape of
// NFR-1 is exercised by TestNFR1FirstResponseWithinBound.
func TestNFR1DefaultWaitBoundCoversBoot(t *testing.T) {
	cfg := mustLoad(t, "# defaults\n", nil)

	const bootTime = 20 * time.Second
	if cfg.WaitBound != 120*time.Second {
		t.Fatalf("default WaitBound = %v, want 120s", cfg.WaitBound)
	}
	if detection := cfg.ProbeInterval + cfg.ProbeTimeout; detection >= cfg.WaitBound {
		t.Fatalf("probe detection %v must be well inside the %v bound", detection, cfg.WaitBound)
	}
	if cfg.WaitBound <= bootTime {
		t.Fatalf("wait bound %v does not cover the %v boot", cfg.WaitBound, bootTime)
	}
}
