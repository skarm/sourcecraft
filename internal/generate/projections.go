package main

import (
	_ "embed"
	"slices"
)

// platformTypes records the existing optional-scalar representation of canonical
// object models. A new definition defaults to value optionals, independently of
// endpoint usage. The input projection always uses pointer optionals.
//
//go:embed platform_types.json
var platformTypes []byte

func (g *generator) planProjections() {
	// Seed models whose own optional fields require a different input type.
	for name := range g.inputs {
		s := g.spec.Definitions[name]
		if s.Type != typeObject {
			continue
		}

		for field, property := range s.Properties {
			required := slices.Contains(s.Required, field)
			if g.needsPointer(property, true, required) != g.needsPointer(property, g.pointerOptionals[name], required) {
				g.projections[name] = true
			}
		}
	}
	// Propagate through parents, arrays and maps to a fixed point. This handles
	// cycles without recursively expanding definitions or depending on map order.
	for changed := true; changed; {
		changed = false

		for name := range g.inputs {
			if !g.projections[name] && g.referencesProjection(g.spec.Definitions[name]) {
				g.projections[name], changed = true, true
			}
		}
	}
}

func (g *generator) referencesProjection(s *schema) bool {
	if s == nil {
		return false
	}

	if s.Ref != "" {
		return g.projections[refName(s.Ref)]
	}

	for _, property := range s.Properties {
		if g.referencesProjection(property) {
			return true
		}
	}

	return g.referencesProjection(s.Items) || g.referencesProjection(s.AdditionalProperties)
}
