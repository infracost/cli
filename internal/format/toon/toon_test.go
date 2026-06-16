package toon

import (
	"encoding/json"
	"testing"
)

// marshalString is a test helper that encodes v and returns the result as a
// string, failing the test on error.
func marshalString(t *testing.T, v any) string {
	t.Helper()
	b, err := Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return string(b)
}

func TestEmptyObject(t *testing.T) {
	got := marshalString(t, map[string]any{})
	if got != "" {
		t.Fatalf("empty object should produce empty document, got %q", got)
	}
}

func TestObjectPrimitives(t *testing.T) {
	type S struct {
		ID     int    `json:"id"`
		Name   string `json:"name"`
		Active bool   `json:"active"`
	}
	got := marshalString(t, S{ID: 123, Name: "Ada", Active: true})
	want := "id: 123\nname: Ada\nactive: true"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNestedObject(t *testing.T) {
	type User struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type Wrap struct {
		User User `json:"user"`
	}
	got := marshalString(t, Wrap{User: User{ID: 123, Name: "Ada"}})
	want := "user:\n  id: 123\n  name: Ada"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPrimitiveArrayInline(t *testing.T) {
	type S struct {
		Tags []string `json:"tags"`
	}
	got := marshalString(t, S{Tags: []string{"admin", "ops", "dev"}})
	want := "tags[3]: admin,ops,dev"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmptyArray(t *testing.T) {
	type S struct {
		Tags []string `json:"tags"`
	}
	got := marshalString(t, S{Tags: []string{}})
	want := "tags[0]:"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTabularArray(t *testing.T) {
	type Item struct {
		SKU   string  `json:"sku"`
		Qty   int     `json:"qty"`
		Price float64 `json:"price"`
	}
	type S struct {
		Items []Item `json:"items"`
	}
	got := marshalString(t, S{Items: []Item{
		{SKU: "A1", Qty: 2, Price: 9.99},
		{SKU: "B2", Qty: 1, Price: 14.5},
	}})
	want := "items[2]{sku,qty,price}:\n  A1,2,9.99\n  B2,1,14.5"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMixedArrayExpanded(t *testing.T) {
	// Mixed array (per spec): primitive, object, primitive
	got := marshalString(t, map[string]any{
		"items": []any{
			1,
			map[string]any{"a": 1},
			"text",
		},
	})
	want := "items[3]:\n  - 1\n  - a: 1\n  - text"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestObjectsAsListItemsNonUniform(t *testing.T) {
	// Two objects with different shapes → non-uniform → expanded list, first
	// field on hyphen line.
	got := marshalString(t, map[string]any{
		"items": []any{
			map[string]any{"id": 1, "name": "First"},
			map[string]any{"id": 2, "name": "Second", "extra": true},
		},
	})
	// Map iteration order from json.Decoder preservation: when the input is a
	// map[string]any in Go, our mapToNode sorts keys. So fields will appear
	// alphabetically. For the first object: id, name. Second: extra, id, name.
	// First field on hyphen line, rest at depth+1.
	want := "items[2]:\n  - id: 1\n    name: First\n  - extra: true\n    id: 2\n    name: Second"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNestedTabularInsideListItem(t *testing.T) {
	// Spec §10: when a list-item object's first field is a tabular array, the
	// tabular header MUST be on the hyphen line; rows at depth+2; sibling
	// fields at depth+1. We construct this with a struct (so field order is
	// stable: Users first, Status second).
	type User struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	type Item struct {
		Users  []User `json:"users"`
		Status string `json:"status"`
	}
	type S struct {
		Items []Item `json:"items"`
	}
	got := marshalString(t, S{Items: []Item{{
		Users:  []User{{ID: 1, Name: "Ada"}, {ID: 2, Name: "Bob"}},
		Status: "active",
	}}})
	want := "items[1]:\n  - users[2]{id,name}:\n      1,Ada\n      2,Bob\n    status: active"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestStringQuoting(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"", `""`},
		{"true", `"true"`},
		{"false", `"false"`},
		{"null", `"null"`},
		{"42", `"42"`},
		{"-5", `"-5"`},
		{"3.14", `"3.14"`},
		{"1e6", `"1e6"`},
		{"05", `"05"`},
		{"-foo", `"-foo"`},
		{"hello world", "hello world"},
		{" leading", `" leading"`},
		{"trailing ", `"trailing "`},
		{"with,comma", `"with,comma"`},
		{"with:colon", `"with:colon"`},
		{`with"quote`, `"with\"quote"`},
		{`with\backslash`, `"with\\backslash"`},
		{"with\nnewline", `"with\nnewline"`},
		{"emoji 🎉 ok", "emoji 🎉 ok"},
	}
	for _, c := range cases {
		got := encodeString(c.in, ',')
		if got != c.want {
			t.Errorf("encodeString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestKeyEncoding(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"id", "id"},
		{"my_key", "my_key"},
		{"a.b", "a.b"},
		{"my-key", `"my-key"`},
		{"with space", `"with space"`},
		{"123leading", `"123leading"`},
	}
	for _, c := range cases {
		got := encodeKey(c.in)
		if got != c.want {
			t.Errorf("encodeKey(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNumberCanonicalization(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{1.0, "k: 1"},
		{1.5, "k: 1.5"},
		{1.5000, "k: 1.5"},
		{1e6, "k: 1000000"},
		{0.0000001, "k: 0.0000001"},
		{int64(42), "k: 42"},
		{int64(-7), "k: -7"},
	}
	for _, c := range cases {
		got := marshalString(t, map[string]any{"k": c.in})
		if got != c.want {
			t.Errorf("number %v: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestNaNAndInfBecomeNull(t *testing.T) {
	got := marshalString(t, map[string]any{
		"nan":    nanValue(),
		"posinf": posInf(),
		"neginf": negInf(),
	})
	want := "nan: null\nneginf: null\nposinf: null"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestJSONMarshalerSupport(t *testing.T) {
	// A type whose MarshalJSON returns a string (modeling rat.Rat).
	type S struct {
		Price money `json:"price"`
	}
	got := marshalString(t, S{Price: money("9.99")})
	want := `price: "9.99"`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestOmitemptyAndSkip(t *testing.T) {
	type S struct {
		A string `json:"a,omitempty"`
		B string `json:"b"`
		C string `json:"-"`
		D *int   `json:"d,omitempty"`
	}
	got := marshalString(t, S{B: "kept", C: "skipped"})
	want := "b: kept"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMapKeysSorted(t *testing.T) {
	got := marshalString(t, map[string]string{
		"zebra": "z",
		"apple": "a",
		"mango": "m",
	})
	want := "apple: a\nmango: m\nzebra: z"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTabularDetectionFailsOnNestedValues(t *testing.T) {
	// Nested object inside an array element disqualifies tabular form.
	got := marshalString(t, map[string]any{
		"items": []any{
			map[string]any{"id": 1, "meta": map[string]any{"n": 1}},
			map[string]any{"id": 2, "meta": map[string]any{"n": 2}},
		},
	})
	want := "items[2]:\n  - id: 1\n    meta:\n      n: 1\n  - id: 2\n    meta:\n      n: 2"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRootArrayInline(t *testing.T) {
	got := marshalString(t, []int{1, 2, 3})
	want := "[3]: 1,2,3"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRootArrayTabular(t *testing.T) {
	type Row struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	got := marshalString(t, []Row{{1, "Ada"}, {2, "Bob"}})
	want := "[2]{id,name}:\n  1,Ada\n  2,Bob"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// --- helpers ---------------------------------------------------------------

type money string

func (m money) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(m))
}

// math.NaN/Inf constructed via funcs to avoid go vet complaints in const ctx.
func nanValue() float64 { return zero() / zero() }
func posInf() float64   { return 1.0 / zero() }
func negInf() float64   { return -1.0 / zero() }

func zero() float64 { return 0.0 }
