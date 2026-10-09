package schema

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// forbiddenStrict is REQ-TOOL-03's rejection list. It is checked against
// modelled fields AND against Extra keys, because Extra is exactly how $ref
// gets in.
var forbiddenStrict = []string{
	"$ref", "$defs", "allOf", "oneOf", "not",
	"patternProperties", "prefixItems", "if", "then", "else", "dependentSchemas",
}

func marshalSchema(s *Schema) ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	var b bytes.Buffer
	b.WriteByte('{')
	first := true
	kv := func(k string, raw []byte) {
		if !first {
			b.WriteByte(',')
		}
		first = false
		km, _ := json.Marshal(k)
		b.Write(km)
		b.WriteByte(':')
		b.Write(raw)
	}
	str := func(k, v string) {
		if v == "" {
			return
		}
		vm, _ := json.Marshal(v)
		kv(k, vm)
	}

	if s.Type != TypeNone {
		if s.Nullable {
			arr, _ := json.Marshal([]string{string(s.Type), string(TypeNull)})
			kv("type", arr)
		} else {
			tm, _ := json.Marshal(string(s.Type))
			kv("type", tm)
		}
	}
	str("title", s.Title)
	str("description", s.Description)

	if s.Type == TypeObject || len(s.Properties) > 0 {
		var pb bytes.Buffer
		pb.WriteByte('{')
		for i, name := range s.PropertyList() {
			if i > 0 {
				pb.WriteByte(',')
			}
			nm, _ := json.Marshal(name)
			pb.Write(nm)
			pb.WriteByte(':')
			sub, err := marshalSchema(s.Properties[name])
			if err != nil {
				return nil, err
			}
			pb.Write(sub)
		}
		pb.WriteByte('}')
		kv("properties", pb.Bytes())
	}
	if s.Required != nil {
		rm, _ := json.Marshal(s.Required)
		kv("required", rm)
	}
	if s.AdditionalProperties != nil {
		if s.AdditionalProperties.Schema != nil {
			sub, err := marshalSchema(s.AdditionalProperties.Schema)
			if err != nil {
				return nil, err
			}
			kv("additionalProperties", sub)
		} else {
			kv("additionalProperties", []byte(strconv.FormatBool(s.AdditionalProperties.Allowed)))
		}
	}
	if s.Items != nil {
		sub, err := marshalSchema(s.Items)
		if err != nil {
			return nil, err
		}
		kv("items", sub)
	}
	if len(s.Enum) > 0 {
		var eb bytes.Buffer
		eb.WriteByte('[')
		for i, e := range s.Enum {
			if i > 0 {
				eb.WriteByte(',')
			}
			eb.Write(e)
		}
		eb.WriteByte(']')
		kv("enum", eb.Bytes())
	}
	if s.HasConst {
		kv("const", s.Const)
	}
	for _, g := range []struct {
		k string
		v []*Schema
	}{{"anyOf", s.AnyOf}, {"oneOf", s.OneOf}, {"allOf", s.AllOf}} {
		if len(g.v) == 0 {
			continue
		}
		var ab bytes.Buffer
		ab.WriteByte('[')
		for i, a := range g.v {
			if i > 0 {
				ab.WriteByte(',')
			}
			sub, err := marshalSchema(a)
			if err != nil {
				return nil, err
			}
			ab.Write(sub)
		}
		ab.WriteByte(']')
		kv(g.k, ab.Bytes())
	}
	for _, n := range []struct {
		k string
		v *float64
	}{{"minimum", s.Minimum}, {"maximum", s.Maximum},
		{"exclusiveMinimum", s.ExclusiveMinimum}, {"exclusiveMaximum", s.ExclusiveMaximum},
		{"multipleOf", s.MultipleOf}} {
		if n.v != nil {
			kv(n.k, []byte(strconv.FormatFloat(*n.v, 'g', -1, 64)))
		}
	}
	for _, n := range []struct {
		k string
		v *int
	}{{"minLength", s.MinLength}, {"maxLength", s.MaxLength},
		{"minItems", s.MinItems}, {"maxItems", s.MaxItems}} {
		if n.v != nil {
			kv(n.k, []byte(strconv.Itoa(*n.v)))
		}
	}
	str("pattern", s.Pattern)
	str("format", s.Format)
	if s.UniqueItems != nil {
		kv("uniqueItems", []byte(strconv.FormatBool(*s.UniqueItems)))
	}
	// Extra last, in authored order.
	for _, m := range s.Extra {
		kv(m.Key, m.Value)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func strictSubset(s *Schema) (*Schema, error) {
	c := s.Clone()
	if err := strictCheck(c, ""); err != nil {
		return nil, err
	}
	strictRewrite(c)
	return c, nil
}

func strictCheck(s *Schema, path string) error {
	if s == nil {
		return nil
	}
	if len(s.AllOf) > 0 {
		return &StrictRewriteError{Path: pathOr(path), Keyword: "allOf"}
	}
	if len(s.OneOf) > 0 {
		return &StrictRewriteError{Path: pathOr(path), Keyword: "oneOf"}
	}
	for _, m := range s.Extra {
		for _, f := range forbiddenStrict {
			if m.Key == f {
				return &StrictRewriteError{Path: pathOr(path), Keyword: f}
			}
		}
	}
	// A dictionary object — additionalProperties carrying a SCHEMA — has no
	// strict form. The strict subset requires additionalProperties:false and
	// every key enumerated in properties, and a map of arbitrary keys is
	// exactly what cannot be enumerated. The rewrite used to overwrite the
	// value schema with `false` and call the result strict, which made the
	// model unable to emit any key at all; so it is a rejection, which lets
	// `prefer` fall back to the unconstrained schema and `require` fail with
	// the reason instead of shipping a tool that cannot be called.
	if s.AdditionalProperties != nil && s.AdditionalProperties.Schema != nil {
		return &StrictRewriteError{Path: pathOr(path), Keyword: "additionalProperties",
			Reason: "additionalProperties carries a schema (a dictionary object), which the " +
				"strict subset cannot express: strict requires every key to be enumerated"}
	}
	// An array is only strict with its items declared. Array(nil) marshals as
	// a bare {"type":"array"}, which strict mode rejects on the wire.
	if s.Type == TypeArray && s.Items == nil {
		return &StrictRewriteError{Path: pathOr(path), Keyword: "items",
			Reason: "array declares no items schema, which the strict subset requires"}
	}
	for _, name := range s.PropertyList() {
		if err := strictCheck(s.Properties[name], path+"/"+name); err != nil {
			return err
		}
	}
	if err := strictCheck(s.Items, path+"/items"); err != nil {
		return err
	}
	for i, a := range s.AnyOf {
		if err := strictCheck(a, path+"/anyOf/"+strconv.Itoa(i)); err != nil {
			return err
		}
	}
	return nil
}

func pathOr(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}

func strictRewrite(s *Schema) {
	if s == nil {
		return
	}
	// A schema with properties and no type is an object in every reader's
	// eyes and in marshalSchema's; the rewrite treats it as one and pins the
	// type, because strict mode wants the word on the wire.
	if s.Type == TypeNone && len(s.Properties) > 0 {
		s.Type = TypeObject
	}
	if s.Type == TypeObject {
		s.AdditionalProperties = &AdditionalProperties{Allowed: false}
		all := s.PropertyList()
		for _, name := range all {
			if !s.IsRequired(name) {
				p := s.Properties[name]
				// Widen a formerly-optional, non-nullable property so the
				// model can still omit it. The description moves UP to the
				// wrapper and is cleared on the inner copy: duplicated, it
				// is model-visible twice, once per level, and the wrapper is
				// the property the model sees.
				if p != nil && !p.Nullable {
					desc := p.Description
					p.Description = ""
					s.Properties[name] = &Schema{
						Description: desc,
						AnyOf:       []*Schema{p, {Type: TypeNull}},
					}
				}
			}
		}
		s.Required = append([]string(nil), all...)
	}
	for _, name := range s.PropertyList() {
		strictRewrite(s.Properties[name])
	}
	strictRewrite(s.Items)
	for _, a := range s.AnyOf {
		strictRewrite(a)
	}
}

// The argument functions work on the value encoding/json produces: nil,
// bool, string, json.Number (or any Go number), []any and map[string]any.

func deleteOptionalNulls(s *Schema, in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		var sub *Schema
		if s != nil {
			sub = s.Properties[k]
			if v == nil && sub != nil && !s.IsRequired(k) {
				continue
			}
		}
		out[k] = descendNulls(sub, v)
	}
	return out
}

func descendNulls(s *Schema, v any) any {
	switch t := v.(type) {
	case map[string]any:
		return deleteOptionalNulls(s, t)
	case []any:
		var item *Schema
		if s != nil {
			item = s.Items
		}
		arr := make([]any, len(t))
		for i := range t {
			arr[i] = descendNulls(item, t[i])
		}
		return arr
	}
	return v
}

func coerce(s *Schema, in map[string]any) (map[string]any, []Coercion) {
	var log []Coercion
	out := make(map[string]any, len(in))
	for _, k := range sortedKeys(in) {
		var sub *Schema
		if s != nil {
			sub = s.Properties[k]
		}
		v, l := coerceValue(sub, in[k], k)
		log = append(log, l...)
		out[k] = v
	}
	return out, log
}

func coerceValue(s *Schema, v any, path string) (any, []Coercion) {
	if s == nil {
		return v, nil
	}
	// StrictSubset writes an optional property as anyOf[T, null]. A value
	// coerces through that shape as it would through T alone.
	if s.Type == "" {
		if only := soleNonNullBranch(s.AnyOf); only != nil {
			s = only
		}
	}
	switch t := v.(type) {
	case map[string]any:
		return coerce(s, t)
	case []any:
		var log []Coercion
		arr := make([]any, len(t))
		for i := range t {
			e, l := coerceValue(s.Items, t[i], path+"/"+strconv.Itoa(i))
			arr[i] = e
			log = append(log, l...)
		}
		return arr, log
	case string:
		switch s.Type {
		case TypeInteger, TypeNumber:
			// Surrounding space and trailing separators are dropped first: a
			// model writing read_file's offset as "1, " meant 1. What is left
			// becomes a number only if it IS a JSON number: ParseFloat also
			// takes "NaN", "+5", ".5", "5." and "1_0", and writing those
			// verbatim made the arguments invalid JSON. Anything else is left
			// as the string for validation to refuse.
			num := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t), ",;"))
			if !jsonNumber.MatchString(num) {
				break
			}
			if s.Type == TypeInteger {
				if n, err := strconv.ParseInt(num, 10, 64); err == nil {
					return n, []Coercion{{Path: path, From: TypeString, To: s.Type}}
				}
				if isIntegral(num) {
					return json.Number(num), []Coercion{{Path: path, From: TypeString, To: s.Type}}
				}
				break
			}
			if f, err := strconv.ParseFloat(num, 64); err == nil {
				return f, []Coercion{{Path: path, From: TypeString, To: s.Type}}
			}
		case TypeBoolean:
			if b, err := strconv.ParseBool(t); err == nil {
				return b, []Coercion{{Path: path, From: TypeString, To: TypeBoolean}}
			}
		}
	default:
		if text, ok := numberText(v); ok && s.Type == TypeString {
			return text, []Coercion{{Path: path, From: TypeNumber, To: TypeString}}
		}
	}
	return v, nil
}

// numberText is v's JSON number literal, when v is a number.
func numberText(v any) (string, bool) {
	switch n := v.(type) {
	case json.Number:
		return n.String(), true
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(n), 'g', -1, 32), true
	case int:
		return strconv.Itoa(n), true
	case int8, int16, int32, int64:
		return fmt.Sprint(n), true
	case uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(n), true
	}
	return "", false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// jsonNumber is RFC 8259's number grammar.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// isIntegral reports whether a JSON number literal has an integral value
// (2, 2.0 and 2e3 do; 1.5 does not).
func isIntegral(num string) bool {
	f, err := strconv.ParseFloat(num, 64)
	return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
}

// soleNonNullBranch is the one branch of alts that is not {type: null}, or nil
// when there is not exactly one.
func soleNonNullBranch(alts []*Schema) *Schema {
	var only *Schema
	for _, a := range alts {
		if a == nil || (a.Type == TypeNull && len(a.AnyOf) == 0) {
			continue
		}
		if only != nil {
			return nil
		}
		only = a
	}
	return only
}

func validate(s *Schema, v any) error {
	var issues []Issue
	if m, ok := v.(map[string]any); ok && s != nil && s.Type == "" {
		// A bare object schema (no type) is still a set of properties.
		validateObject(s, m, "", &issues)
	} else {
		validateValue(s, v, "", &issues)
	}
	if len(issues) == 0 {
		return nil
	}
	args, _ := json.Marshal(v)
	return &ValidationError{Issues: issues, Args: args}
}

func validateObject(s *Schema, in map[string]any, path string, issues *[]Issue) {
	if s == nil {
		return
	}
	for _, req := range s.Required {
		if _, ok := in[req]; !ok {
			*issues = append(*issues, Issue{Path: join(path, req), Message: "required property is missing"})
		}
	}
	for _, k := range sortedKeys(in) {
		sub := s.Properties[k]
		if sub == nil {
			// additionalProperties: false is a promise strict mode makes to
			// the model; a key outside the declared set breaks it.
			if ap := s.AdditionalProperties; ap != nil {
				switch {
				case !ap.Allowed:
					*issues = append(*issues, Issue{Path: join(path, k),
						Message: "property not allowed: this object accepts only its declared properties"})
				case ap.Schema != nil:
					validateValue(ap.Schema, in[k], join(path, k), issues)
				}
			}
			continue
		}
		validateValue(sub, in[k], join(path, k), issues)
	}
}

// validateValue checks v against everything s declares: type (nullable
// included, for containers as for scalars), const and enum, the anyOf, oneOf
// and allOf combinators, numeric bounds and integrality, string lengths and
// pattern, and item counts and uniqueness. The keywords beyond `type` are
// what strict mode promises the model, and a promise the SDK does not check
// is a promise to nobody.
func validateValue(s *Schema, v any, path string, issues *[]Issue) {
	if s == nil {
		return
	}
	if v == nil && s.Nullable {
		return
	}
	add := func(msg string) { *issues = append(*issues, Issue{Path: path, Message: msg}) }

	if s.HasConst && !sameJSON(v, s.Const) {
		add("must be " + string(s.Const))
		return
	}
	if len(s.Enum) > 0 {
		match := false
		for _, e := range s.Enum {
			if sameJSON(v, e) {
				match = true
				break
			}
		}
		if !match {
			add("must be one of " + enumList(s.Enum) + ", got " + preview(v))
			return
		}
	}
	if len(s.AnyOf) > 0 {
		passing := 0
		for _, alt := range s.AnyOf {
			if conforms(alt, v) {
				passing++
				break
			}
		}
		if passing == 0 {
			add("matches no anyOf alternative (got " + kindName(v) + ")")
			return
		}
	}
	if len(s.OneOf) > 0 {
		passing := 0
		for _, alt := range s.OneOf {
			if conforms(alt, v) {
				passing++
			}
		}
		if passing != 1 {
			add(fmt.Sprintf("must match exactly one oneOf alternative, matches %d", passing))
			return
		}
	}
	for _, all := range s.AllOf {
		validateValue(all, v, path, issues)
	}

	switch s.Type {
	case TypeObject:
		m, ok := v.(map[string]any)
		if !ok {
			add("expected object, got " + kindName(v) + sentAsString(v))
			return
		}
		validateObject(s, m, path, issues)
	case TypeArray:
		arr, ok := v.([]any)
		if !ok {
			add("expected array, got " + kindName(v) + sentAsString(v))
			return
		}
		validateArray(s, arr, path, add, issues)
	case TypeString:
		str, ok := v.(string)
		if !ok {
			add("expected string, got " + kindName(v))
			return
		}
		validateString(s, str, add)
	case TypeInteger, TypeNumber:
		validateNumber(s, v, add)
	case TypeBoolean:
		if _, ok := v.(bool); !ok {
			add("expected boolean, got " + kindName(v))
		}
	case TypeNull:
		if v != nil {
			add("expected null, got " + kindName(v))
		}
	}
}

func validateArray(s *Schema, arr []any, path string, add func(string), issues *[]Issue) {
	if s.MinItems != nil && len(arr) < *s.MinItems {
		add(fmt.Sprintf("expected at least %d items, got %d", *s.MinItems, len(arr)))
	}
	if s.MaxItems != nil && len(arr) > *s.MaxItems {
		add(fmt.Sprintf("expected at most %d items, got %d", *s.MaxItems, len(arr)))
	}
	if s.UniqueItems != nil && *s.UniqueItems {
		seen := make(map[string]int, len(arr))
		for i, e := range arr {
			key := canonical(e)
			if j, dup := seen[key]; dup {
				add(fmt.Sprintf("items must be unique: item %d repeats item %d", i, j))
				break
			}
			seen[key] = i
		}
	}
	if s.Items != nil {
		for i := range arr {
			validateValue(s.Items, arr[i], path+"/"+strconv.Itoa(i), issues)
		}
	}
}

func validateString(s *Schema, str string, add func(string)) {
	n := utf8.RuneCountInString(str)
	if s.MinLength != nil && n < *s.MinLength {
		add(fmt.Sprintf("expected at least %d characters, got %d", *s.MinLength, n))
	}
	if s.MaxLength != nil && n > *s.MaxLength {
		add(fmt.Sprintf("expected at most %d characters, got %d", *s.MaxLength, n))
	}
	if s.Pattern != "" {
		re, err := regexp.Compile(s.Pattern)
		switch {
		case err != nil:
			add("the schema's pattern does not compile: " + err.Error())
		case !re.MatchString(str):
			add("must match pattern " + strconv.Quote(s.Pattern) + ", got " + quotedPreview(str))
		}
	}
}

func validateNumber(s *Schema, v any, add func(string)) {
	text, ok := numberText(v)
	if !ok {
		msg := "expected " + string(s.Type) + ", got " + kindName(v)
		if str, isStr := v.(string); isStr {
			msg += " " + quotedPreview(str) + "; pass a number, e.g. 120"
		}
		add(msg)
		return
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil {
		add("expected " + string(s.Type) + ", got " + text)
		return
	}
	if s.Type == TypeInteger && f != math.Trunc(f) {
		add("expected integer, got " + text)
		return
	}
	bound := func(ok bool, rel string, b float64) {
		if !ok {
			add(fmt.Sprintf("must be %s %s, got %s", rel, strconv.FormatFloat(b, 'g', -1, 64), text))
		}
	}
	if s.Minimum != nil {
		bound(f >= *s.Minimum, "at least", *s.Minimum)
	}
	if s.Maximum != nil {
		bound(f <= *s.Maximum, "at most", *s.Maximum)
	}
	if s.ExclusiveMinimum != nil {
		bound(f > *s.ExclusiveMinimum, "greater than", *s.ExclusiveMinimum)
	}
	if s.ExclusiveMaximum != nil {
		bound(f < *s.ExclusiveMaximum, "less than", *s.ExclusiveMaximum)
	}
	if s.MultipleOf != nil && *s.MultipleOf > 0 {
		q := f / *s.MultipleOf
		bound(q == math.Trunc(q), "a multiple of", *s.MultipleOf)
	}
}

// conforms reports whether v satisfies s, for the combinators.
func conforms(s *Schema, v any) bool {
	var issues []Issue
	validateValue(s, v, "", &issues)
	return len(issues) == 0
}

// canonical is v's JSON with object keys sorted and numbers by value, so two
// values equal in meaning compare equal as strings.
func canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	var norm any
	if err := json.Unmarshal(b, &norm); err != nil {
		return string(b)
	}
	out, _ := json.Marshal(norm)
	return string(out)
}

// sameJSON compares a value with a raw JSON literal by meaning, not bytes:
// 1 and 1.0 are equal, key order does not matter.
func sameJSON(v any, raw json.RawMessage) bool {
	var want any
	if err := json.Unmarshal(raw, &want); err != nil {
		return false
	}
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	var got any
	if err := json.Unmarshal(b, &got); err != nil {
		return false
	}
	return reflect.DeepEqual(got, want)
}

func enumList(vals []json.RawMessage) string {
	parts := make([]string, len(vals))
	for i, v := range vals {
		parts[i] = string(v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// preview renders a value for an issue: a string quoted and abbreviated, any
// other value as its JSON.
func preview(v any) string {
	if str, ok := v.(string); ok {
		return quotedPreview(str)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "/" + k
}

func kindName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	if _, ok := numberText(v); ok {
		return "number"
	}
	return "object"
}

// echoPreviewRunes bounds each string value echoed back in a validation
// error, and the received value quoted in an issue.
const echoPreviewRunes = 80

// sentAsString is the hint for a structure delivered as a string — an array
// or object the model JSON-encoded into a string — with the start of what
// arrived.
func sentAsString(v any) string {
	str, ok := v.(string)
	if !ok {
		return ""
	}
	return ": pass the value itself, not a JSON string (received " + quotedPreview(str) + ")"
}

// quotedPreview is a string, quoted, cut at echoPreviewRunes.
func quotedPreview(str string) string {
	q, _ := json.Marshal(abbreviate(str))
	return string(q)
}

// abbreviate cuts s at echoPreviewRunes runes, marking the cut.
func abbreviate(s string) string {
	n := 0
	for i := range s {
		if n == echoPreviewRunes {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// abbreviatedJSON is raw with every long string value abbreviated, keys and
// their order untouched: it is re-encoded token by token.
func abbreviatedJSON(raw []byte) []byte {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	var out bytes.Buffer
	enc := jsontext.NewEncoder(&out)
	for {
		tok, err := dec.ReadToken()
		if err != nil {
			break
		}
		if tok.Kind() == '"' {
			str := tok.String()
			if short := abbreviate(str); short != str && !isName(dec) {
				tok = jsontext.String(short)
			}
		}
		if enc.WriteToken(tok) != nil {
			return raw
		}
	}
	return bytes.TrimSpace(out.Bytes())
}

// isName reports whether the token just read is an object member's name: in
// an object, names are the odd-numbered tokens.
func isName(dec *jsontext.Decoder) bool {
	kind, n := dec.StackIndex(dec.StackDepth())
	return kind == '{' && n%2 == 1
}

// renderValidationError echoes the model's OWN arguments in the model's own
// key order, which is what makes the correction self-serving (REQ-TOOL-12.3).
// Long string values are abbreviated: the error is about the arguments'
// shape, and echoing a kilobyte of edit text back buries the one line that
// says what was wrong — once per failed call.
func renderValidationError(e *ValidationError) string {
	var b strings.Builder
	b.WriteString("Invalid arguments:\n")
	for _, is := range e.Issues {
		b.WriteString("  - ")
		b.WriteString(is.Path)
		b.WriteString(": ")
		b.WriteString(is.Message)
		b.WriteByte('\n')
	}
	b.WriteString("Arguments received:\n")
	b.Write(abbreviatedJSON(e.Args))
	return b.String()
}
