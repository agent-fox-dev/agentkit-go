package codemode

import (
	"fmt"
	"sort"
	"strings"

	"github.com/agent-fox-dev/agentkit-go/core"
	"github.com/agent-fox-dev/agentkit-go/schema"
)

// RenderSignature declares t as the Starlark function a script calls: its
// keyword parameters from InputSchema (required first, optional ones
// defaulting to None), its return shape from OutputSchema (Any when it has
// none), and its description and parameter descriptions as a docstring.
// It is generated from the tool's own schemas, so the declaration cannot
// drift from what the tool accepts and returns.
func RenderSignature(t core.Tool) string {
	var required, optional, docs []string
	if in := t.InputSchema; in != nil {
		for _, name := range orderedProps(in) {
			p := in.Properties[name]
			if in.IsRequired(name) {
				required = append(required, name+": "+starlarkType(p))
			} else {
				optional = append(optional, name+": "+starlarkType(p)+" = None")
			}
			if p != nil && p.Description != "" {
				docs = append(docs, name+": "+oneLine(p.Description))
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "def %s(%s) -> %s:\n", t.Name, strings.Join(append(required, optional...), ", "), returnType(t.OutputSchema))
	b.WriteString(`    """`)
	if t.Description != "" {
		b.WriteString(oneLine(t.Description))
	}
	for _, d := range docs {
		b.WriteString("\n    " + d)
	}
	b.WriteString(`"""`)
	return b.String()
}

// orderedProps is an object's properties in declaration order, with any not
// in PropertyOrder after them, sorted.
func orderedProps(s *schema.Schema) []string {
	names := append([]string(nil), s.PropertyOrder...)
	listed := make(map[string]bool, len(names))
	for _, n := range names {
		listed[n] = true
	}
	var rest []string
	for n := range s.Properties {
		if !listed[n] {
			rest = append(rest, n)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// starlarkType is the Starlark type a script passes or receives for s.
func starlarkType(s *schema.Schema) string {
	if s == nil {
		return "Any"
	}
	switch s.Type {
	case schema.TypeString:
		return "str"
	case schema.TypeInteger:
		return "int"
	case schema.TypeNumber:
		return "float"
	case schema.TypeBoolean:
		return "bool"
	case schema.TypeArray:
		if s.Items != nil && s.Items.Type != schema.TypeNone && s.Items.Type != schema.TypeObject {
			return "list[" + starlarkType(s.Items) + "]"
		}
		return "list"
	case schema.TypeObject:
		return "dict"
	case schema.TypeNull:
		return "None"
	}
	return "Any"
}

// returnType renders an output schema as the shape of the dict a call
// returns: {name: type, ...}, optional fields as "type | None", nested objects
// and arrays of objects spelled out two levels deep. A union lists its
// alternatives.
func returnType(s *schema.Schema) string {
	if s == nil {
		return "Any"
	}
	if len(s.OneOf) > 0 || len(s.AnyOf) > 0 {
		alts := s.OneOf
		if len(alts) == 0 {
			alts = s.AnyOf
		}
		parts := make([]string, len(alts))
		for i, a := range alts {
			parts[i] = returnType(a)
		}
		return strings.Join(parts, " | ")
	}
	return shape(s, 3)
}

func shape(s *schema.Schema, depth int) string {
	switch {
	case s == nil:
		return "Any"
	case s.Type == schema.TypeObject && len(s.Properties) > 0 && depth > 0:
		fields := make([]string, 0, len(s.Properties))
		for _, name := range orderedProps(s) {
			typ := shape(s.Properties[name], depth-1)
			if !s.IsRequired(name) {
				typ += " | None"
			}
			fields = append(fields, name+": "+typ)
		}
		return "{" + strings.Join(fields, ", ") + "}"
	case s.Type == schema.TypeArray && s.Items != nil && s.Items.Type == schema.TypeObject && depth > 0:
		return "list[" + shape(s.Items, depth-1) + "]"
	}
	return starlarkType(s)
}

// oneLine folds a description onto one line for a signature's docstring,
// escaping a triple quote that would end the docstring early.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.Join(strings.Fields(s), " "), `"""`, `\"\"\"`)
}
