package main

import (
	"encoding/json"
	"strings"
)

type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	Format               string             `json:"format"`
	Title                string             `json:"title"`
	Description          string             `json:"description"`
	Properties           map[string]*schema `json:"properties"`
	Items                *schema            `json:"items"`
	AdditionalProperties *schema            `json:"additionalProperties"`
	Enum                 []string           `json:"enum"`
	Required             []string           `json:"required"`
	AllOf                []json.RawMessage  `json:"allOf"`
	AnyOf                []json.RawMessage  `json:"anyOf"`
	OneOf                []json.RawMessage  `json:"oneOf"`
}

type parameter struct {
	Name             string  `json:"name"`
	In               string  `json:"in"`
	Required         bool    `json:"required"`
	Description      string  `json:"description"`
	Type             string  `json:"type"`
	Format           string  `json:"format"`
	Items            *schema `json:"items"`
	CollectionFormat string  `json:"collectionFormat"`
	Schema           *schema `json:"schema"`
}

type operation struct {
	ID          string      `json:"operationId"`
	Summary     string      `json:"summary"`
	Description string      `json:"description"`
	Tags        []string    `json:"tags"`
	Parameters  []parameter `json:"parameters"`
	Responses   map[string]struct {
		Schema *schema `json:"schema"`
	} `json:"responses"`
	RequestBody *struct {
		Content map[string]struct {
			Schema *schema `json:"schema"`
		} `json:"content"`
	} `json:"requestBody"`
}

type specification struct {
	Swagger     string                          `json:"swagger"`
	Paths       map[string]map[string]operation `json:"paths"`
	Definitions map[string]*schema              `json:"definitions"`
}

type generator struct {
	spec specification
	// Canonical models use a pinned policy; input reachability only selects new
	// request projections and never changes a canonical model's representation.
	pointerOptionals map[string]bool
	inputs           map[string]bool
	projections      map[string]bool
}

func (g *generator) markInput(s *schema) {
	if s == nil {
		return
	}

	rejectPlatformComposition(s)

	if s.Ref != "" {
		name := refName(s.Ref)
		if g.inputs[name] {
			return
		}

		g.inputs[name] = true
		s = g.resolve(s)
	}

	for _, property := range s.Properties {
		g.markInput(property)
	}

	if s.Items != nil {
		g.markInput(s.Items)
	}

	if s.AdditionalProperties != nil {
		g.markInput(s.AdditionalProperties)
	}
}

func (g *generator) goType(s *schema) string {
	return g.projectedType(s, false)
}

func (g *generator) inputType(s *schema) string {
	return g.projectedType(s, true)
}

func (g *generator) projectedType(s *schema, input bool) string {
	if s == nil {
		panic("missing type schema")
	}

	rejectPlatformComposition(s)

	if s.Ref != "" {
		g.resolve(s)

		name := refName(s.Ref)
		if input && g.projections[name] {
			return goName(name) + "Input"
		}

		return goName(name)
	}

	switch s.Type {
	case typeString:
		switch s.Format {
		case "date-time":
			return "time.Time"
		case typeByte:
			return "[]byte"
		case typeInt64:
			return typeInt64
		case typeUint64:
			return typeUint64
		case "", "date", formatUUID, "binary":
			return typeString
		default:
			panic("unsupported string format: " + s.Format)
		}
	case typeBoolean:
		return typeBool
	case typeInteger:
		if s.Format == typeInt32 {
			return typeInt32
		}

		if s.Format == typeUint64 {
			return typeUint64
		}

		return typeInt64
	case typeNumber:
		if s.Format == "float" {
			return "float32"
		}

		return typeFloat64
	case typeArray:
		g.rejectQuotedInteger(s.Items)
		return "[]" + g.projectedType(s.Items, input)
	case typeObject:
		if s.AdditionalProperties != nil {
			if len(s.Properties) != 0 {
				panic("mixed object properties and dictionary entries are unsupported")
			}

			g.rejectQuotedInteger(s.AdditionalProperties)

			return "map[string]" + g.projectedType(s.AdditionalProperties, input)
		}

		if len(s.Properties) == 0 {
			return "EmptyResponse"
		}

		panic("inline object properties are unsupported")
	case "":
		return "json.RawMessage"
	default:
		panic("unsupported schema type: " + s.Type)
	}
}

func (g *generator) needsPointer(s *schema, pointerOptionals, required bool) bool {
	resolved := g.resolve(s)
	if s.Ref != "" && resolved.Type == typeObject {
		return true
	}

	if resolved.Type == typeString && resolved.Format == "date-time" {
		return !required
	}

	if !pointerOptionals || required {
		return false
	}

	return resolved.Type == typeBoolean || resolved.Type == typeInteger || resolved.Type == typeNumber || (resolved.Type == typeString && resolved.Format != typeByte)
}

func (g *generator) resolve(s *schema) *schema {
	if s == nil {
		panic("missing schema")
	}

	rejectPlatformComposition(s)

	if s.Ref == "" {
		return s
	}

	definition := g.spec.Definitions[refName(s.Ref)]
	if definition == nil {
		panic("missing definition: " + s.Ref)
	}

	rejectPlatformComposition(definition)

	return definition
}

func refName(ref string) string {
	const prefix = "#/definitions/"
	if !strings.HasPrefix(ref, prefix) {
		panic("unsupported reference: " + ref)
	}

	return strings.TrimPrefix(ref, prefix)
}

// String-encoded integers rely on encoding/json's ",string" struct-field tag.
// A scalar body, response, array item or dictionary value has no such tag.
func (g *generator) rejectQuotedInteger(s *schema) {
	resolved := g.resolve(s)
	if resolved.Type == typeString && (resolved.Format == typeInt64 || resolved.Format == typeUint64) {
		panic("string-encoded integers require numeric model fields")
	}
}

func rejectPlatformComposition(s *schema) {
	if len(s.AllOf) != 0 || len(s.AnyOf) != 0 || len(s.OneOf) != 0 {
		panic("Swagger schema composition is unsupported")
	}
}
