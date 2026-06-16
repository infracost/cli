// Package toon encodes Go values as TOON (Token-Oriented Object Notation), a
// compact, indentation-based format designed for LLM prompts. See
// https://github.com/toon-format/spec for the format specification.
//
// Only encoding is implemented (decoding is out of scope for the CLI).
// The encoder uses comma as the delimiter and 2-space indentation. Struct
// field names follow encoding/json conventions: the `json` tag is honored,
// including the `omitempty` option and `-` to skip a field. Types that
// implement json.Marshaler are encoded using their JSON representation,
// keeping TOON semantically aligned with the existing --json output.
package toon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Marshal encodes v as a TOON document.
func Marshal(v any) ([]byte, error) {
	n, err := toNode(reflect.ValueOf(v))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := encodeRoot(&buf, n, 2); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// MarshalTo writes the TOON encoding of v to w.
func MarshalTo(w io.Writer, v any) error {
	b, err := Marshal(v)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// node is the intermediate representation produced from a Go value before
// rendering. It mirrors the JSON data model with primitives, arrays, and
// objects whose keys preserve encounter order.
type node struct {
	kind nodeKind
	str  string // for kString and kNumber
	b    bool   // for kBool
	arr  []node
	obj  []field
}

type nodeKind int

const (
	kNull nodeKind = iota
	kBool
	kNumber
	kString
	kArray
	kObject
)

type field struct {
	key string
	val node
}

// ----- value → node conversion ----------------------------------------------

var jsonMarshalerType = reflect.TypeFor[json.Marshaler]()

func toNode(v reflect.Value) (node, error) {
	// Unwrap interface and pointer layers; nil → null.
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return node{kind: kNull}, nil
		}
		v = v.Elem()
	}

	if !v.IsValid() {
		return node{kind: kNull}, nil
	}

	// Honor json.Marshaler so types like rat.Rat encode the same way they do
	// in --json output (preserving wire semantics with that flag).
	if m, ok := jsonMarshalerOf(v); ok {
		b, err := m.MarshalJSON()
		if err != nil {
			return node{}, err
		}
		return decodeJSON(b)
	}

	switch v.Kind() {
	case reflect.Bool:
		return node{kind: kBool, b: v.Bool()}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return node{kind: kNumber, str: strconv.FormatInt(v.Int(), 10)}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return node{kind: kNumber, str: strconv.FormatUint(v.Uint(), 10)}, nil
	case reflect.Float32, reflect.Float64:
		return numberFromFloat(v.Float()), nil
	case reflect.String:
		return node{kind: kString, str: v.String()}, nil
	case reflect.Slice, reflect.Array:
		// []byte handling: emit as base64 string would diverge from json's
		// default. The infracost output struct does not use []byte, so we
		// fall through to the generic array path.
		out := make([]node, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			n, err := toNode(v.Index(i))
			if err != nil {
				return node{}, err
			}
			out = append(out, n)
		}
		return node{kind: kArray, arr: out}, nil
	case reflect.Map:
		return mapToNode(v)
	case reflect.Struct:
		return structToNode(v)
	}

	return node{}, fmt.Errorf("toon: unsupported kind %s", v.Kind())
}

func jsonMarshalerOf(v reflect.Value) (json.Marshaler, bool) {
	if v.Type().Implements(jsonMarshalerType) {
		return v.Interface().(json.Marshaler), true
	}
	if v.CanAddr() && v.Addr().Type().Implements(jsonMarshalerType) {
		return v.Addr().Interface().(json.Marshaler), true
	}
	return nil, false
}

func numberFromFloat(f float64) node {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		// Per spec §3, NaN and ±Infinity normalize to null.
		return node{kind: kNull}
	}
	if f == 0 {
		// Normalize -0 to 0.
		f = 0
	}
	// strconv with -1 precision picks the shortest form that round-trips.
	// 'f' avoids exponent notation per spec §2.
	s := strconv.FormatFloat(f, 'f', -1, 64)
	return node{kind: kNumber, str: s}
}

func structToNode(v reflect.Value) (node, error) {
	t := v.Type()
	fields := make([]field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		if !ft.IsExported() {
			continue
		}
		name, omit, skip := parseJSONTag(ft)
		if skip {
			continue
		}
		fv := v.Field(i)
		if omit && isZeroForJSON(fv) {
			continue
		}
		n, err := toNode(fv)
		if err != nil {
			return node{}, err
		}
		fields = append(fields, field{key: name, val: n})
	}
	return node{kind: kObject, obj: fields}, nil
}

func mapToNode(v reflect.Value) (node, error) {
	if v.IsNil() {
		return node{kind: kNull}, nil
	}
	if v.Type().Key().Kind() != reflect.String {
		return node{}, fmt.Errorf("toon: map keys must be strings, got %s", v.Type().Key().Kind())
	}
	keys := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		keys = append(keys, k.String())
	}
	// Maps have no encounter order; sort for determinism.
	sort.Strings(keys)
	fields := make([]field, 0, len(keys))
	for _, k := range keys {
		n, err := toNode(v.MapIndex(reflect.ValueOf(k)))
		if err != nil {
			return node{}, err
		}
		fields = append(fields, field{key: k, val: n})
	}
	return node{kind: kObject, obj: fields}, nil
}

func parseJSONTag(f reflect.StructField) (name string, omit, skip bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	if tag == "" {
		return f.Name, false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omit = true
		}
	}
	return name, omit, false
}

// isZeroForJSON mirrors encoding/json's "empty" check for omitempty: false,
// 0, nil pointers/interfaces, and zero-length arrays/slices/maps/strings.
func isZeroForJSON(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool:
		return !v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int() == 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return v.Uint() == 0
	case reflect.Float32, reflect.Float64:
		return v.Float() == 0
	case reflect.Interface, reflect.Pointer:
		return v.IsNil()
	}
	return false
}

// decodeJSON parses raw JSON bytes into a node tree, preserving key order in
// objects. Used for json.Marshaler outputs.
func decodeJSON(data []byte) (node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return node{}, err
	}
	return decodeJSONValue(dec, tok)
}

func decodeJSONValue(dec *json.Decoder, tok json.Token) (node, error) {
	switch t := tok.(type) {
	case nil:
		return node{kind: kNull}, nil
	case bool:
		return node{kind: kBool, b: t}, nil
	case json.Number:
		// Re-canonicalize via float to drop exponent notation per spec §2.
		// json.Number preserves source text; if it contains 'e' or trailing
		// zeros we want to normalize.
		s := canonicalNumber(string(t))
		return node{kind: kNumber, str: s}, nil
	case string:
		return node{kind: kString, str: t}, nil
	case json.Delim:
		switch t {
		case '[':
			arr := []node{}
			for dec.More() {
				inner, err := dec.Token()
				if err != nil {
					return node{}, err
				}
				n, err := decodeJSONValue(dec, inner)
				if err != nil {
					return node{}, err
				}
				arr = append(arr, n)
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return node{}, err
			}
			return node{kind: kArray, arr: arr}, nil
		case '{':
			fs := []field{}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return node{}, err
				}
				key, ok := keyTok.(string)
				if !ok {
					return node{}, fmt.Errorf("toon: non-string object key %v", keyTok)
				}
				valTok, err := dec.Token()
				if err != nil {
					return node{}, err
				}
				val, err := decodeJSONValue(dec, valTok)
				if err != nil {
					return node{}, err
				}
				fs = append(fs, field{key: key, val: val})
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return node{}, err
			}
			return node{kind: kObject, obj: fs}, nil
		}
	}
	return node{}, fmt.Errorf("toon: unexpected JSON token %v", tok)
}

// canonicalNumber normalizes a numeric token to the canonical decimal form
// required by spec §2 (no exponents, no trailing zeros, -0 → 0).
func canonicalNumber(s string) string {
	// Fast path: if there's no exponent and no trailing zeros after a dot,
	// and no leading minus zero, we can return as-is. But it's simpler to
	// always re-format via float64.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		// Could be an integer that overflows float64. Try int64.
		if i, ierr := strconv.ParseInt(s, 10, 64); ierr == nil {
			return strconv.FormatInt(i, 10)
		}
		// Last resort: keep the source verbatim.
		return s
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "0"
	}
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// ----- node → TOON encoding -------------------------------------------------

func encodeRoot(w *bytes.Buffer, n node, indent int) error {
	switch n.kind {
	case kObject:
		if len(n.obj) == 0 {
			return nil // empty object → empty document (spec §8)
		}
		return encodeObjectFields(w, n.obj, 0, indent)
	case kArray:
		// Root array. Emit "[N]:" header then content.
		return encodeRootArray(w, n.arr, indent)
	default:
		// Single primitive at root: emit on a single line per §5.
		s, err := encodePrimitive(n, ',')
		if err != nil {
			return err
		}
		w.WriteString(s)
		return nil
	}
}

func encodeRootArray(w *bytes.Buffer, arr []node, indent int) error {
	switch arrayShape(arr) {
	case shapeEmpty:
		w.WriteString("[0]:")
	case shapeInline:
		w.WriteString("[")
		w.WriteString(strconv.Itoa(len(arr)))
		w.WriteString("]: ")
		writeInline(w, arr)
	case shapeTabular:
		fields := tabularFields(arr)
		w.WriteString("[")
		w.WriteString(strconv.Itoa(len(arr)))
		w.WriteString("]{")
		writeFieldList(w, fields)
		w.WriteString("}:")
		writeTabularRows(w, arr, fields, 1, indent)
	default: // shapeExpanded
		w.WriteString("[")
		w.WriteString(strconv.Itoa(len(arr)))
		w.WriteString("]:")
		writeExpandedItems(w, arr, 1, indent)
	}
	return nil
}

func encodeObjectFields(w *bytes.Buffer, fs []field, depth, indent int) error {
	for i, f := range fs {
		if i > 0 {
			w.WriteByte('\n')
		}
		writeIndent(w, depth, indent)
		writeKey(w, f.key)
		writeFieldBody(w, f.val, depth, indent)
	}
	return nil
}

// writeFieldBody emits the part of a field after the key (excluding indentation
// and the key itself). It does NOT add a trailing newline for nested content;
// the next field's leading newline (in encodeObjectFields) handles separation.
func writeFieldBody(w *bytes.Buffer, n node, depth, indent int) {
	switch n.kind {
	case kObject:
		if len(n.obj) == 0 {
			w.WriteByte(':')
			return
		}
		w.WriteByte(':')
		w.WriteByte('\n')
		_ = encodeObjectFields(w, n.obj, depth+1, indent)
	case kArray:
		writeArrayField(w, n.arr, depth, indent)
	default:
		w.WriteString(": ")
		s, _ := encodePrimitive(n, ',')
		w.WriteString(s)
	}
}

func writeArrayField(w *bytes.Buffer, arr []node, depth, indent int) {
	switch arrayShape(arr) {
	case shapeEmpty:
		w.WriteString("[0]:")
	case shapeInline:
		w.WriteByte('[')
		w.WriteString(strconv.Itoa(len(arr)))
		w.WriteString("]: ")
		writeInline(w, arr)
	case shapeTabular:
		fields := tabularFields(arr)
		w.WriteByte('[')
		w.WriteString(strconv.Itoa(len(arr)))
		w.WriteString("]{")
		writeFieldList(w, fields)
		w.WriteString("}:")
		writeTabularRows(w, arr, fields, depth+1, indent)
	default:
		w.WriteByte('[')
		w.WriteString(strconv.Itoa(len(arr)))
		w.WriteString("]:")
		writeExpandedItems(w, arr, depth+1, indent)
	}
}

func writeInline(w *bytes.Buffer, arr []node) {
	for i, el := range arr {
		if i > 0 {
			w.WriteByte(',')
		}
		s, _ := encodePrimitive(el, ',')
		w.WriteString(s)
	}
}

func writeFieldList(w *bytes.Buffer, fields []string) {
	for i, f := range fields {
		if i > 0 {
			w.WriteByte(',')
		}
		w.WriteString(encodeKey(f))
	}
}

func writeTabularRows(w *bytes.Buffer, arr []node, fields []string, depth, indent int) {
	for _, row := range arr {
		w.WriteByte('\n')
		writeIndent(w, depth, indent)
		// row is an object with the given fields (possibly in different order).
		for i, fname := range fields {
			if i > 0 {
				w.WriteByte(',')
			}
			val := lookupField(row, fname)
			s, _ := encodePrimitive(val, ',')
			w.WriteString(s)
		}
	}
}

func writeExpandedItems(w *bytes.Buffer, arr []node, depth, indent int) {
	for _, item := range arr {
		w.WriteByte('\n')
		writeIndent(w, depth, indent)
		w.WriteString("- ")
		writeListItem(w, item, depth, indent)
	}
}

// writeListItem renders the body of a "- " line for an expanded array item.
// The leading "- " has already been written.
func writeListItem(w *bytes.Buffer, n node, depth, indent int) {
	switch n.kind {
	case kObject:
		if len(n.obj) == 0 {
			// Bare hyphen on its own line (we already wrote "- "; trim it).
			// Easiest: don't write anything — but we already wrote "- ".
			// Walk back: replace last two bytes ("- ") with "-".
			b := w.Bytes()
			if len(b) >= 2 && b[len(b)-1] == ' ' && b[len(b)-2] == '-' {
				w.Truncate(len(b) - 1)
			}
			return
		}
		// Per §10: if first field's value is a tabular array, encoders MUST
		// emit the tabular header on the hyphen line; rows at depth+2; other
		// fields at depth+1.
		first := n.obj[0]
		if first.val.kind == kArray && arrayShape(first.val.arr) == shapeTabular {
			fields := tabularFields(first.val.arr)
			writeKey(w, first.key)
			w.WriteByte('[')
			w.WriteString(strconv.Itoa(len(first.val.arr)))
			w.WriteString("]{")
			writeFieldList(w, fields)
			w.WriteString("}:")
			writeTabularRows(w, first.val.arr, fields, depth+2, indent)
			// remaining fields at depth+1
			for _, rf := range n.obj[1:] {
				w.WriteByte('\n')
				writeIndent(w, depth+1, indent)
				writeKey(w, rf.key)
				writeFieldBody(w, rf.val, depth+1, indent)
			}
			return
		}
		// Default: first field on hyphen line, rest at depth+1.
		writeKey(w, first.key)
		writeFieldBody(w, first.val, depth, indent)
		for _, rf := range n.obj[1:] {
			w.WriteByte('\n')
			writeIndent(w, depth+1, indent)
			writeKey(w, rf.key)
			writeFieldBody(w, rf.val, depth+1, indent)
		}
	case kArray:
		// Array as list item.
		switch arrayShape(n.arr) {
		case shapeEmpty:
			w.WriteString("[0]:")
		case shapeInline:
			w.WriteByte('[')
			w.WriteString(strconv.Itoa(len(n.arr)))
			w.WriteString("]: ")
			writeInline(w, n.arr)
		case shapeTabular:
			// No key here — array as a list item without a key. Render expanded.
			fields := tabularFields(n.arr)
			w.WriteByte('[')
			w.WriteString(strconv.Itoa(len(n.arr)))
			w.WriteString("]{")
			writeFieldList(w, fields)
			w.WriteString("}:")
			writeTabularRows(w, n.arr, fields, depth+1, indent)
		default:
			w.WriteByte('[')
			w.WriteString(strconv.Itoa(len(n.arr)))
			w.WriteString("]:")
			writeExpandedItems(w, n.arr, depth+1, indent)
		}
	default:
		// Primitive list item: "- value"
		s, _ := encodePrimitive(n, ',')
		w.WriteString(s)
	}
}

func writeIndent(w *bytes.Buffer, depth, indent int) {
	for i := 0; i < depth*indent; i++ {
		w.WriteByte(' ')
	}
}

func writeKey(w *bytes.Buffer, k string) {
	w.WriteString(encodeKey(k))
}

// ----- shape detection ------------------------------------------------------

type arrShape int

const (
	shapeEmpty arrShape = iota
	shapeInline
	shapeTabular
	shapeExpanded
)

// arrayShape decides which TOON form to use for an array.
func arrayShape(arr []node) arrShape {
	if len(arr) == 0 {
		return shapeEmpty
	}
	allPrim := true
	allObj := true
	for _, el := range arr {
		if el.kind == kObject {
			allPrim = false
		} else {
			allObj = false
		}
		if el.kind == kArray {
			allPrim = false
		}
	}
	if allPrim {
		return shapeInline
	}
	if allObj && uniformObjects(arr) {
		return shapeTabular
	}
	return shapeExpanded
}

func uniformObjects(arr []node) bool {
	if len(arr) == 0 {
		return false
	}
	// All values across all keys must be primitives (no nested objects/arrays).
	first := arr[0]
	keySet := make(map[string]struct{}, len(first.obj))
	for _, f := range first.obj {
		if !isPrimitive(f.val) {
			return false
		}
		keySet[f.key] = struct{}{}
	}
	for _, el := range arr[1:] {
		if len(el.obj) != len(keySet) {
			return false
		}
		seen := make(map[string]struct{}, len(keySet))
		for _, f := range el.obj {
			if _, ok := keySet[f.key]; !ok {
				return false
			}
			if _, dup := seen[f.key]; dup {
				return false
			}
			seen[f.key] = struct{}{}
			if !isPrimitive(f.val) {
				return false
			}
		}
	}
	return true
}

func isPrimitive(n node) bool {
	return n.kind == kNull || n.kind == kBool || n.kind == kNumber || n.kind == kString
}

// tabularFields returns the canonical field order: encounter order in the
// first object.
func tabularFields(arr []node) []string {
	if len(arr) == 0 {
		return nil
	}
	out := make([]string, 0, len(arr[0].obj))
	for _, f := range arr[0].obj {
		out = append(out, f.key)
	}
	return out
}

func lookupField(obj node, key string) node {
	for _, f := range obj.obj {
		if f.key == key {
			return f.val
		}
	}
	return node{kind: kNull}
}

// ----- primitive encoding & quoting -----------------------------------------

func encodePrimitive(n node, activeDelim byte) (string, error) {
	switch n.kind {
	case kNull:
		return "null", nil
	case kBool:
		if n.b {
			return "true", nil
		}
		return "false", nil
	case kNumber:
		return n.str, nil
	case kString:
		return encodeString(n.str, activeDelim), nil
	}
	return "", fmt.Errorf("toon: not a primitive: kind=%d", n.kind)
}

var numericLikeRe = regexp.MustCompile(`^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$`)
var leadingZeroRe = regexp.MustCompile(`^0\d+$`)
var unquotedKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

// encodeString applies the quoting rules in spec §7.2. activeDelim is the
// delimiter governing the current scope (',' for the document and tabular
// rows / inline arrays under comma headers).
func encodeString(s string, activeDelim byte) string {
	if needsQuoting(s, activeDelim) {
		return quote(s)
	}
	return s
}

func needsQuoting(s string, activeDelim byte) bool {
	if s == "" {
		return true
	}
	if s == "true" || s == "false" || s == "null" {
		return true
	}
	if s[0] == '-' {
		return true
	}
	if numericLikeRe.MatchString(s) || leadingZeroRe.MatchString(s) {
		return true
	}
	// Leading or trailing whitespace.
	if hasLeadingOrTrailingWhitespace(s) {
		return true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case ':', '"', '\\', '[', ']', '{', '}', '\n', '\r', '\t':
			return true
		}
		if c == activeDelim {
			return true
		}
	}
	return false
}

func hasLeadingOrTrailingWhitespace(s string) bool {
	if s == "" {
		return false
	}
	if isWS(s[0]) || isWS(s[len(s)-1]) {
		return true
	}
	return false
}

func isWS(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

func quote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func encodeKey(k string) string {
	if unquotedKeyRe.MatchString(k) {
		return k
	}
	return quote(k)
}
