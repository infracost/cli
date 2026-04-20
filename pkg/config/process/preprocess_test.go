package process

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func TestPreProcess_DefaultValues(t *testing.T) {
	type cfg struct {
		Name    string `default:"hello"`
		Count   int    `default:"42"`
		Enabled bool   `default:"true"`
	}

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name != "hello" {
		t.Errorf("Name = %q, want %q", c.Name, "hello")
	}
	if c.Count != 42 {
		t.Errorf("Count = %d, want %d", c.Count, 42)
	}
	if !c.Enabled {
		t.Error("Enabled = false, want true")
	}
}

func TestPreProcess_DefaultDoesNotOverwriteExisting(t *testing.T) {
	type cfg struct {
		Name string `default:"hello"`
	}

	c := cfg{Name: "already-set"}
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name != "already-set" {
		t.Errorf("Name = %q, want %q", c.Name, "already-set")
	}
}

func TestPreProcess_EnvVar(t *testing.T) {
	type cfg struct {
		Name string `env:"TEST_PREPROCESS_NAME"`
	}

	t.Setenv("TEST_PREPROCESS_NAME", "from-env")

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name != "from-env" {
		t.Errorf("Name = %q, want %q", c.Name, "from-env")
	}
}

func TestPreProcess_EnvOverridesDefault(t *testing.T) {
	type cfg struct {
		Name string `env:"TEST_PREPROCESS_NAME" default:"fallback"`
	}

	t.Setenv("TEST_PREPROCESS_NAME", "from-env")

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name != "from-env" {
		t.Errorf("Name = %q, want %q", c.Name, "from-env")
	}
}

func TestPreProcess_EnvInvalidValue(t *testing.T) {
	type cfg struct {
		Count int `env:"TEST_PREPROCESS_COUNT"`
	}

	t.Setenv("TEST_PREPROCESS_COUNT", "not-a-number")

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() == 0 {
		t.Fatal("expected diagnostics for invalid env var, got none")
	}
}

func TestPreProcess_FlagRegistration(t *testing.T) {
	type cfg struct {
		Name  string `flag:"name" usage:"the name"`
		Count int    `flag:"count" usage:"the count"`
		Debug bool   `flag:"debug" usage:"enable debug"`
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var c cfg
	diags := PreProcess(&c, flags)
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	for _, name := range []string{"name", "count", "debug"} {
		if flags.Lookup(name) == nil {
			t.Errorf("expected flag %q to be registered", name)
		}
	}
}

func TestPreProcess_FlagHidden(t *testing.T) {
	type cfg struct {
		Secret string `flag:"secret;hidden"`
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var c cfg
	PreProcess(&c, flags)

	f := flags.Lookup("secret")
	require.NotNil(t, f, "expected flag 'secret' to be registered")
	if !f.Hidden {
		t.Error("expected flag 'secret' to be hidden")
	}
}

func TestPreProcess_FlagSetsValue(t *testing.T) {
	type cfg struct {
		Name string `flag:"name" default:"original"`
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var c cfg
	PreProcess(&c, flags)

	if err := flags.Parse([]string{"--name", "overridden"}); err != nil {
		t.Fatal(err)
	}

	if c.Name != "overridden" {
		t.Errorf("Name = %q, want %q", c.Name, "overridden")
	}
}

func TestPreProcess_NestedStruct(t *testing.T) {
	type inner struct {
		Value string `default:"nested-default"`
	}
	type cfg struct {
		Inner inner
	}

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Inner.Value != "nested-default" {
		t.Errorf("Inner.Value = %q, want %q", c.Inner.Value, "nested-default")
	}
}

func TestPreProcess_PointerField(t *testing.T) {
	type cfg struct {
		Name *string `default:"ptr-default"`
	}

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name == nil {
		t.Fatal("Name is nil, expected it to be set")
	}
	if *c.Name != "ptr-default" {
		t.Errorf("*Name = %q, want %q", *c.Name, "ptr-default")
	}
}

func TestPreProcess_BoolEnvSpecialCases(t *testing.T) {
	tests := []struct {
		envVal string
		want   bool
	}{
		{"1", true},
		{"0", false},
		{"true", true},
		{"false", false},
		{"TRUE", true},
		{"FALSE", false},
	}

	for _, tt := range tests {
		t.Run(tt.envVal, func(t *testing.T) {
			type cfg struct {
				Flag bool `env:"TEST_PREPROCESS_BOOL"`
			}

			t.Setenv("TEST_PREPROCESS_BOOL", tt.envVal)

			var c cfg
			diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
			if diags.Len() > 0 {
				t.Fatalf("unexpected diagnostics: %s", diags)
			}
			if c.Flag != tt.want {
				t.Errorf("Flag = %v, want %v (env=%q)", c.Flag, tt.want, tt.envVal)
			}
		})
	}
}

func TestPreProcess_DurationDefault(t *testing.T) {
	type cfg struct {
		Timeout time.Duration `default:"5s"`
	}

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want %v", c.Timeout, 5*time.Second)
	}
}

func TestPreProcess_UnexportedFieldsIgnored(t *testing.T) {
	type cfg struct {
		exported   string `default:"should-be-ignored"` //nolint:unused
		unexported string `default:"also-ignored"`      //nolint:unused
		Name       string `default:"visible"`
	}

	var c cfg
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name != "visible" {
		t.Errorf("Name = %q, want %q", c.Name, "visible")
	}
}

func TestPreProcess_NoTags(t *testing.T) {
	type cfg struct {
		Name string
	}

	c := cfg{Name: "untouched"}
	diags := PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
	if diags.Len() > 0 {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	if c.Name != "untouched" {
		t.Errorf("Name = %q, want %q", c.Name, "untouched")
	}
}

func TestPreProcess_PanicOnNonPointer(t *testing.T) {
	type cfg struct {
		Name string `default:"hello"`
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when passing non-pointer")
		}
	}()

	var c cfg
	PreProcess(c, pflag.NewFlagSet("test", pflag.ContinueOnError))
}

func TestPreProcess_PanicOnTaggedNestedStruct(t *testing.T) {
	type inner struct {
		Value string
	}
	type cfg struct {
		Inner inner `env:"BAD"`
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when nested struct has env tag")
		}
	}()

	var c cfg
	PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
}

// testSharedFlag is a minimal SharedFlag implementation for testing.
type testSharedFlag struct {
	value   string
	targets []*string
}

func (f *testSharedFlag) AddTarget(target *string) {
	f.targets = append(f.targets, target)
}

func (f *testSharedFlag) String() string { return f.value }

func (f *testSharedFlag) Set(s string) error {
	f.value = s
	for _, t := range f.targets {
		*t = s
	}
	return nil
}

func (f *testSharedFlag) Type() string { return "string" }

func TestPreProcess_FlagValue(t *testing.T) {
	type owner struct {
		Env testSharedFlag `flag:"env"`
	}
	type consumer struct {
		Env string `flagvalue:"env"`
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	var o owner
	PreProcess(&o, flags)

	var c consumer
	PreProcess(&c, flags)

	if err := flags.Parse([]string{"--env", "prod"}); err != nil {
		t.Fatal(err)
	}

	if o.Env.value != "prod" {
		t.Errorf("owner Env = %q, want %q", o.Env.value, "prod")
	}
	if c.Env != "prod" {
		t.Errorf("consumer Env = %q, want %q", c.Env, "prod")
	}
}

func TestPreProcess_FlagValueMultipleConsumers(t *testing.T) {
	type owner struct {
		Env testSharedFlag `flag:"env"`
	}
	type consumer struct {
		Env string `flagvalue:"env"`
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	var o owner
	var c1, c2 consumer
	PreProcess(&o, flags)
	PreProcess(&c1, flags)
	PreProcess(&c2, flags)

	if err := flags.Parse([]string{"--env", "dev"}); err != nil {
		t.Fatal(err)
	}

	if o.Env.value != "dev" {
		t.Errorf("owner Env = %q, want %q", o.Env.value, "dev")
	}
	if c1.Env != "dev" {
		t.Errorf("consumer1 Env = %q, want %q", c1.Env, "dev")
	}
	if c2.Env != "dev" {
		t.Errorf("consumer2 Env = %q, want %q", c2.Env, "dev")
	}
}

func TestPreProcess_FlagValuePanicUnregistered(t *testing.T) {
	type cfg struct {
		Env string `flagvalue:"env"`
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for flagvalue referencing unregistered flag")
		}
	}()

	var c cfg
	PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
}

func TestPreProcess_FlagValuePanicNonSharedFlag(t *testing.T) {
	type owner struct {
		Env string `flag:"env"`
	}
	type consumer struct {
		Env string `flagvalue:"env"`
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for flagvalue referencing non-SharedFlag")
		}
	}()

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	var o owner
	PreProcess(&o, flags)
	var c consumer
	PreProcess(&c, flags)
}

func TestPreProcess_FlagValuePanicCombinedTags(t *testing.T) {
	type cfg struct {
		Env string `flagvalue:"env" default:"bad"`
	}

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for flagvalue combined with other tags")
		}
	}()

	var c cfg
	PreProcess(&c, pflag.NewFlagSet("test", pflag.ContinueOnError))
}

func TestPreProcess_FlagValueDefaultPropagated(t *testing.T) {
	type owner struct {
		Env testSharedFlag `flag:"env" default:"prod"`
	}
	type consumer struct {
		Env string `flagvalue:"env"`
	}

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)

	var o owner
	PreProcess(&o, flags)

	var c consumer
	PreProcess(&c, flags)

	// Don't parse any flags — the default should propagate to the consumer.
	if o.Env.value != "prod" {
		t.Errorf("owner Env = %q, want %q", o.Env.value, "prod")
	}
	if c.Env != "prod" {
		t.Errorf("consumer Env = %q, want %q", c.Env, "prod")
	}
}
