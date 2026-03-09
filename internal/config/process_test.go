package config

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/infracost/cli/pkg/environment"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

// our existing config should always work with Process with no flags or environment variables.
func TestProcess_RealConfig(t *testing.T) {
	var config Config
	diags := Process(&config, pflag.NewFlagSet("", pflag.ContinueOnError))
	require.Empty(t, diags)

	// just some simple checks against known default values and the like

	require.Equal(t, environment.Production, config.Environment)
}

func TestProcess(t *testing.T) {
	tcs := map[string]struct {
		environs    map[string]string
		flags       []string
		hiddenFlags []string
		init        func() (any, any)
	}{
		"no tags, but has values": {
			init: func() (any, any) {
				type config struct {
					Flag string
					Env  string
				}
				return &config{}, &config{}
			},
			environs: map[string]string{
				"ENV": "env",
			},
			flags: []string{"--flag", "flag"},
		},
		"no tags and no values": {
			init: func() (any, any) {
				type config struct {
					Flag string
					Env  string
				}
				return &config{}, &config{}
			},
		},
		"env": {
			init: func() (any, any) {
				type config struct {
					Value string `env:"ENV"`
				}
				return &config{}, &config{Value: "env"}
			},
			environs: map[string]string{
				"ENV": "env",
			},
		},
		"flags": {
			init: func() (any, any) {
				type config struct {
					Value string `flag:"flag"`
				}
				return &config{}, &config{Value: "flag"}
			},
			flags: []string{"--flag", "flag"},
		},
		"flag values take priority": {
			init: func() (any, any) {
				type config struct {
					Value string `flag:"flag" env:"ENV"`
				}
				return &config{}, &config{Value: "flag"}
			},
			environs: map[string]string{
				"ENV": "env",
			},
			flags: []string{"--flag", "flag"},
		},
		"hidden flag": {
			init: func() (any, any) {
				type config struct {
					Value string `flag:"hidden-flag;hidden"`
				}
				return &config{}, &config{Value: "flag"}
			},
			flags:       []string{"--hidden-flag", "flag"},
			hiddenFlags: []string{"hidden-flag"},
		},
		"embedded struct": {
			init: func() (any, any) {
				type embedded struct {
					Env  string `env:"EMBEDDED_ENV"`
					Flag string `flag:"embedded-flag"`
				}
				type config struct {
					Root     string `env:"ROOT_ENV"`
					Embedded embedded
				}
				return &config{}, &config{
					Root: "root",
					Embedded: embedded{
						Env:  "env",
						Flag: "flag",
					},
				}
			},
			environs: map[string]string{
				"ROOT_ENV":     "root",
				"EMBEDDED_ENV": "env",
			},
			flags: []string{"--embedded-flag", "flag"},
		},
		"embedded pointer to struct": {
			init: func() (any, any) {
				type embedded struct {
					Env  string `env:"EMBEDDED_ENV"`
					Flag string `flag:"embedded-flag"`
				}
				type config struct {
					Root     string `env:"ROOT_ENV"`
					Embedded *embedded
				}
				return &config{}, &config{
					Root: "root",
					Embedded: &embedded{
						Env:  "env",
						Flag: "flag",
					},
				}
			},
			environs: map[string]string{
				"ROOT_ENV":     "root",
				"EMBEDDED_ENV": "env",
			},
			flags: []string{"--embedded-flag", "flag"},
		},
		"embedded pointer to pointer to struct": {
			init: func() (any, any) {
				type embedded struct {
					Env  string `env:"EMBEDDED_ENV"`
					Flag string `flag:"embedded-flag"`
				}
				type config struct {
					Root     string `env:"ROOT_ENV"`
					Embedded **embedded
				}
				e := &embedded{
					Env:  "env",
					Flag: "flag",
				}
				pe := &e
				return &config{}, &config{
					Root:     "root",
					Embedded: pe,
				}
			},
			environs: map[string]string{
				"ROOT_ENV":     "root",
				"EMBEDDED_ENV": "env",
			},
			flags: []string{"--embedded-flag", "flag"},
		},
		"basic types - env": {
			init: func() (any, any) {
				type config struct {
					String string `env:"STRING" flag:"string"`
					Int    int    `env:"INT" flag:"int"`
					Bool   bool   `env:"BOOL" flag:"bool"`
				}
				return &config{}, &config{
					String: "hello",
					Int:    42,
					Bool:   true,
				}
			},
			environs: map[string]string{
				"STRING": "hello",
				"INT":    "42",
				"BOOL":   "true",
			},
		},
		"basic types (pointers) - env": {
			init: func() (any, any) {
				type config struct {
					String *string `env:"STRING" flag:"string"`
					Int    *int    `env:"INT" flag:"int"`
					Bool   **bool  `env:"BOOL" flag:"bool"`
				}

				s, i, b := "hello", 42, true
				bb := &b
				return &config{}, &config{
					String: &s,
					Int:    &i,
					Bool:   &bb,
				}
			},
			environs: map[string]string{
				"STRING": "hello",
				"INT":    "42",
				"BOOL":   "true",
			},
		},
		"basic types (pointers) - no values, only flags": {
			init: func() (any, any) {
				type config struct {
					String *string `flag:"string"`
					Int    *int    `flag:"int"`
					Bool   **bool  `flag:"bool"`
				}

				s, i, b := "", 0, false
				bb := &b
				return &config{}, &config{
					String: &s,
					Int:    &i,
					Bool:   &bb,
				}
			},
		},
		"basic types (pointers) - no values, only env": {
			init: func() (any, any) {
				type config struct {
					String *string `env:"STRING"`
					Int    *int    `env:"INT"`
					Bool   **bool  `env:"BOOL"`
				}
				return &config{}, &config{}
			},
		},
		"basic types - flags": {
			init: func() (any, any) {
				type config struct {
					String string `env:"STRING" flag:"string"`
					Int    int    `env:"INT" flag:"int"`
					Bool   bool   `env:"BOOL" flag:"bool"`
				}
				return &config{}, &config{
					String: "hello",
					Int:    42,
					Bool:   true,
				}
			},
			flags: []string{"--string", "hello", "--int", "42", "--bool"},
		},
		"custom type - env": {
			init: func() (any, any) {
				type config struct {
					Custom customStruct `env:"CUSTOM"`
				}
				return &config{}, &config{
					Custom: customStruct{Val: "custom-hello"},
				}
			},
			environs: map[string]string{
				"CUSTOM": "hello",
			},
		},
		"custom type (pointer) - env": {
			init: func() (any, any) {
				type config struct {
					Custom *customStruct `env:"CUSTOM"`
				}
				return &config{}, &config{
					Custom: &customStruct{Val: "custom-hello"},
				}
			},
			environs: map[string]string{
				"CUSTOM": "hello",
			},
		},
		"custom type - flags": {
			init: func() (any, any) {
				type config struct {
					Custom customStruct `flag:"custom"`
				}
				return &config{}, &config{
					Custom: customStruct{Val: "custom-hello"},
				}
			},
			flags: []string{"--custom", "hello"},
		},
		"default value": {
			init: func() (any, any) {
				type config struct {
					Value string `default:"my-default"`
				}
				return &config{}, &config{Value: "my-default"}
			},
		},
		"usage tag": {
			init: func() (any, any) {
				type config struct {
					Value string `flag:"my-flag" usage:"my usage description"`
				}
				return &config{}, &config{Value: "flag"}
			},
			flags: []string{"--my-flag", "flag"},
		},
		"custom slice pflag.Value": {
			init: func() (any, any) {
				type config struct {
					Value customSlice `flag:"my-flag"`
				}
				return &config{}, &config{Value: customSlice{"val1", "val2"}}
			},
			flags: []string{"--my-flag", "val1", "--my-flag", "val2"},
		},
	}
	for name, tc := range tcs {
		t.Run(name, func(t *testing.T) {
			for k, v := range tc.environs {
				t.Setenv(k, v)
			}
			flags := pflag.NewFlagSet("", pflag.ContinueOnError)
			flags.ParseErrorsAllowlist.UnknownFlags = true

			got, want := tc.init()
			diags := Process(got, flags)
			require.Empty(t, diags)

			if name == "usage tag" {
				// one off test to make sure the usage is being set correctly.
				f := flags.Lookup("my-flag")
				require.NotNil(t, f)
				require.Equal(t, "my usage description", f.Usage)
			}

			for _, hf := range tc.hiddenFlags {
				f := flags.Lookup(hf)
				require.NotNil(t, f, "flag %s not found", hf)
				require.True(t, f.Hidden, "flag %s is not hidden", hf)
			}

			require.NoError(t, flags.Parse(tc.flags))

			if diff := cmp.Diff(want, got); len(diff) > 0 {
				t.Errorf("unexpected diff: %s", diff)
			}
		})
	}
}

type customSlice []string

func (c *customSlice) String() string {
	if c == nil {
		return ""
	}
	return "[" + string(append([]byte{}, []byte(nil)...)) + "]" // Just a placeholder
}

func (c *customSlice) Set(s string) error {
	*c = append(*c, s)
	return nil
}

func (c *customSlice) Type() string {
	return "customSlice"
}

type customStruct struct {
	Val string
}

func (c *customStruct) String() string {
	return c.Val
}

func (c *customStruct) Set(s string) error {
	c.Val = "custom-" + s
	return nil
}

func (c *customStruct) Type() string {
	return "custom"
}
