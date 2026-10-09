package schema

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
)

// Parse decodes a JSON Schema document. It reads the document token by token
// with encoding/json/jsontext, so the order the author declared properties
// in becomes PropertyOrder, and a keyword Schema does not model is kept in
// Extra, in their authored order. Malformed JSON, trailing data, a duplicate
// key, or a
// modelled keyword holding the wrong kind of value is an error and no
// schema.
func Parse(data []byte) (*Schema, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(data))
	s, err := parseSchema(dec, "")
	if err != nil {
		return nil, err
	}
	if _, err := dec.ReadToken(); err != io.EOF {
		return nil, errors.New("schema: trailing data after the schema")
	}
	return s, nil
}

// parseSchema reads one schema object from dec. path names it in errors.
func parseSchema(dec *jsontext.Decoder, path string) (*Schema, error) {
	if err := expect(dec, '{', path); err != nil {
		return nil, err
	}
	s := &Schema{}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		key := tok.String() // a token is void after the decoder's next call
		if err := parseKeyword(dec, s, key, join(path, key)); err != nil {
			return nil, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, err
	}
	return s, nil
}

// parseKeyword reads the value of one keyword into s.
func parseKeyword(dec *jsontext.Decoder, s *Schema, key, path string) error {
	switch key {
	case "type":
		return parseType(dec, s, path)
	case "description":
		return parseInto(dec, &s.Description, path)
	case "title":
		return parseInto(dec, &s.Title, path)
	case "pattern":
		return parseInto(dec, &s.Pattern, path)
	case "format":
		return parseInto(dec, &s.Format, path)
	case "nullable":
		return parseInto(dec, &s.Nullable, path)
	case "required":
		return parseInto(dec, &s.Required, path)
	case "minimum":
		return parseInto(dec, &s.Minimum, path)
	case "maximum":
		return parseInto(dec, &s.Maximum, path)
	case "exclusiveMinimum":
		return parseInto(dec, &s.ExclusiveMinimum, path)
	case "exclusiveMaximum":
		return parseInto(dec, &s.ExclusiveMaximum, path)
	case "multipleOf":
		return parseInto(dec, &s.MultipleOf, path)
	case "minLength":
		return parseInto(dec, &s.MinLength, path)
	case "maxLength":
		return parseInto(dec, &s.MaxLength, path)
	case "minItems":
		return parseInto(dec, &s.MinItems, path)
	case "maxItems":
		return parseInto(dec, &s.MaxItems, path)
	case "uniqueItems":
		return parseInto(dec, &s.UniqueItems, path)
	case "const":
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		s.Const, s.HasConst = compact(raw), true
		return nil
	case "enum":
		return parseEnum(dec, s, path)
	case "properties":
		return parseProperties(dec, s, path)
	case "additionalProperties":
		return parseAdditional(dec, s, path)
	case "items":
		item, err := parseSchema(dec, path)
		if err != nil {
			return err
		}
		s.Items = item
		return nil
	case "anyOf":
		return parseSchemas(dec, &s.AnyOf, path)
	case "oneOf":
		return parseSchemas(dec, &s.OneOf, path)
	case "allOf":
		return parseSchemas(dec, &s.AllOf, path)
	}
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	s.Extra = append(s.Extra, Keyword{Key: key, Value: compact(raw)})
	return nil
}

// parseType reads "type": a name, or a name and "null" (nullable).
func parseType(dec *jsontext.Decoder, s *Schema, path string) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		s.Type = Type(one)
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return fmt.Errorf("schema: %s: want a type name or a list of them", path)
	}
	for _, t := range many {
		if Type(t) == TypeNull && len(many) > 1 {
			s.Nullable = true
			continue
		}
		if s.Type != TypeNone {
			return fmt.Errorf("schema: %s: a type list may name one type besides null", path)
		}
		s.Type = Type(t)
	}
	return nil
}

// parseInto reads the next value into dst with encoding/json.
func parseInto(dec *jsontext.Decoder, dst any, path string) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("schema: %s: %w", path, err)
	}
	return nil
}

func parseEnum(dec *jsontext.Decoder, s *Schema, path string) error {
	if err := expect(dec, '[', path); err != nil {
		return err
	}
	s.Enum = []json.RawMessage{}
	for dec.PeekKind() != ']' {
		raw, err := dec.ReadValue()
		if err != nil {
			return err
		}
		s.Enum = append(s.Enum, compact(raw))
	}
	_, err := dec.ReadToken()
	return err
}

func parseProperties(dec *jsontext.Decoder, s *Schema, path string) error {
	if err := expect(dec, '{', path); err != nil {
		return err
	}
	s.Properties = map[string]*Schema{}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}
		name := tok.String()
		sub, err := parseSchema(dec, join(path, name))
		if err != nil {
			return err
		}
		// jsontext refuses a duplicate name, so each name arrives once.
		s.PropertyOrder = append(s.PropertyOrder, name)
		s.Properties[name] = sub
	}
	_, err := dec.ReadToken()
	return err
}

// parseAdditional reads additionalProperties: a boolean or a schema.
func parseAdditional(dec *jsontext.Decoder, s *Schema, path string) error {
	switch dec.PeekKind() {
	case 't', 'f':
		var allowed bool
		if err := parseInto(dec, &allowed, path); err != nil {
			return err
		}
		s.AdditionalProperties = &AdditionalProperties{Allowed: allowed}
		return nil
	}
	sub, err := parseSchema(dec, path)
	if err != nil {
		return err
	}
	s.AdditionalProperties = &AdditionalProperties{Allowed: true, Schema: sub}
	return nil
}

func parseSchemas(dec *jsontext.Decoder, dst *[]*Schema, path string) error {
	if err := expect(dec, '[', path); err != nil {
		return err
	}
	*dst = []*Schema{}
	for i := 0; dec.PeekKind() != ']'; i++ {
		sub, err := parseSchema(dec, fmt.Sprintf("%s/%d", path, i))
		if err != nil {
			return err
		}
		*dst = append(*dst, sub)
	}
	_, err := dec.ReadToken()
	return err
}

// expect reads the next token and checks it opens what the caller wants.
func expect(dec *jsontext.Decoder, kind jsontext.Kind, path string) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != kind {
		want := "an object"
		if kind == '[' {
			want = "an array"
		}
		return fmt.Errorf("schema: %s: want %s, got %s", pathOr(path), want, tok.Kind())
	}
	return nil
}

// compact is raw without insignificant whitespace, so MarshalJSON writes a
// kept value the way it writes every other.
func compact(raw []byte) json.RawMessage {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return b.Bytes()
}
