package main

import (
	"go/types"
	"slices"
	"strconv"
	"strings"
)

type apiFormatter struct {
	packagePath    string
	constraint     bool
	constraintPath []types.Type
}

// describe records actual types and constant values, ignoring documentation,
// import aliases, parameter names and private implementation fields.
func describe(pkg *types.Package) surface {
	result := surface{}

	formatter := apiFormatter{packagePath: pkg.Path()}
	for _, name := range pkg.Scope().Names() {
		obj := pkg.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}

		switch obj := obj.(type) {
		case *types.TypeName:
			formatter.recordType(result, obj)
		case *types.Func:
			result["func "+name] = formatter.typeText(obj.Type())
		case *types.Const:
			result["const "+name] = formatter.typeText(obj.Type()) + " = " + obj.Val().ExactString()
		case *types.Var:
			result["var "+name] = formatter.typeText(obj.Type())
		}
	}

	return result
}

func (f apiFormatter) typeText(typ types.Type) string {
	switch typ := typ.(type) {
	case *types.Signature:
		return f.signatureText(typ)
	case *types.Struct:
		return f.structText(typ)
	case *types.Interface:
		return f.interfaceText(typ)
	case *types.Alias:
		if f.constraint && f.localPrivate(typ.Obj()) {
			return f.typeText(typ.Rhs())
		}
	case *types.Named:
		if f.constraint && f.localPrivate(typ.Obj()) {
			return f.privateConstraintText(typ)
		}
	case *types.Union:
		terms := make([]string, 0, typ.Len())
		for term := range typ.Terms() {
			text := f.typeText(term.Type())
			if term.Tilde() {
				text = "~" + text
			}

			terms = append(terms, text)
		}

		return strings.Join(terms, " | ")
	case *types.Pointer:
		return "*" + f.typeText(typ.Elem())
	case *types.Slice:
		return "[]" + f.typeText(typ.Elem())
	case *types.Array:
		return "[" + strconv.FormatInt(typ.Len(), 10) + "]" + f.typeText(typ.Elem())
	case *types.Map:
		return "map[" + f.typeText(typ.Key()) + "]" + f.typeText(typ.Elem())
	case *types.Chan:
		prefix := "chan "

		switch typ.Dir() {
		case types.SendOnly:
			prefix = "chan<- "
		case types.RecvOnly:
			prefix = "<-chan "
		}

		element := f.typeText(typ.Elem())
		if channel, ok := typ.Elem().(*types.Chan); ok && typ.Dir() == types.SendRecv && channel.Dir() == types.RecvOnly {
			element = "(" + element + ")"
		}

		return prefix + element
	}

	return f.qualifiedTypeText(typ)
}

func (f apiFormatter) qualifiedTypeText(typ types.Type) string {
	return types.TypeString(typ, func(pkg *types.Package) string {
		if pkg.Path() == f.packagePath {
			return ""
		}

		return pkg.Path()
	})
}

func (f apiFormatter) signatureText(signature *types.Signature) string {
	prefix := "func"
	if generics := f.typeParamsText(signature.TypeParams()); generics != "" {
		prefix += "[" + generics + "]"
	}

	return prefix + "(" + f.tupleText(signature.Params(), signature.Variadic()) + ") (" + f.tupleText(signature.Results(), false) + ")"
}

func (f apiFormatter) tupleText(tuple *types.Tuple, variadic bool) string {
	var items []string

	for i := 0; i < tuple.Len(); i++ {
		typ := tuple.At(i).Type()
		if !variadic || i != tuple.Len()-1 {
			items = append(items, f.typeText(typ))
			continue
		}

		slice, ok := typ.(*types.Slice)
		if !ok {
			panic("variadic parameter is not a slice in checked Go types")
		}

		items = append(items, "..."+f.typeText(slice.Elem()))
	}

	return strings.Join(items, ", ")
}

func (f apiFormatter) typeParamsText(params *types.TypeParamList) string {
	var generics []string
	for param := range params.TypeParams() {
		generics = append(generics, param.Obj().Name()+" "+f.constraintText(param.Constraint()))
	}

	return strings.Join(generics, ", ")
}

func (f apiFormatter) structText(typ *types.Struct) string {
	var fields []string

	for i := 0; i < typ.NumFields(); i++ {
		field := typ.Field(i)
		if !field.Exported() {
			continue
		}

		kind := "field "
		if field.Embedded() {
			kind = "embedded "
		}

		fields = append(fields, kind+field.Name()+" "+f.typeText(field.Type())+" "+strconv.Quote(typ.Tag(i)))
	}

	return "struct { " + strings.Join(fields, "; ") + " }"
}

func (f apiFormatter) interfaceText(typ *types.Interface) string {
	var members []string
	for method := range typ.ExplicitMethods() {
		members = append(members, method.Name()+" "+f.typeText(method.Type()))
	}

	for embedded := range typ.EmbeddedTypes() {
		members = append(members, f.constraintText(embedded))
	}

	slices.Sort(members)

	return "interface { " + strings.Join(members, "; ") + " }"
}

// Private interface names do not describe their type sets. Expand the local
// constraint definitions while preserving the identity of named concrete terms.
func (f apiFormatter) constraintText(typ types.Type) string {
	f.constraint = true
	return f.typeText(typ)
}

func (f apiFormatter) localPrivate(obj *types.TypeName) bool {
	return !obj.Exported() && obj.Pkg() != nil && obj.Pkg().Path() == f.packagePath
}

func (f apiFormatter) privateConstraintText(typ *types.Named) string {
	if underlying, ok := typ.Underlying().(*types.Interface); ok {
		if slices.Contains(f.constraintPath, types.Type(typ)) {
			return f.qualifiedTypeText(typ)
		}

		f.constraintPath = append(f.constraintPath, typ)

		return f.interfaceText(underlying)
	}
	// Preserve concrete identity and inspect only one underlying level. Recursive
	// structs can refer to themselves, unlike checked interface embedding graphs.
	plain := apiFormatter{packagePath: f.packagePath}

	return f.qualifiedTypeText(typ) + " [comparable=" + strconv.FormatBool(types.Comparable(typ)) + " underlying " + plain.typeText(typ.Underlying()) + "]"
}

func (f apiFormatter) recordType(result surface, obj *types.TypeName) {
	if obj.IsAlias() {
		f.recordAlias(result, obj)
		return
	}

	named, ok := obj.Type().(*types.Named)
	if !ok {
		panic("defined type is not named in checked Go types")
	}

	result["type "+obj.Name()] = "defined [" + f.typeParamsText(named.TypeParams()) + "] comparable=" + strconv.FormatBool(types.Comparable(named)) + " " + f.typeText(named.Underlying())
	for method := range named.Methods() {
		if !method.Exported() {
			continue
		}

		signature, ok := method.Type().(*types.Signature)
		if !ok {
			panic("method has no signature in checked Go types")
		}

		result["method ("+f.typeText(signature.Recv().Type())+")."+method.Name()] = f.typeText(signature)
	}

	if hasEmbedding(named) {
		f.recordMethodSets(result, obj.Name(), named)
		f.recordFieldSet(result, obj.Name(), named, false)
	}
}

func hasEmbedding(typ types.Type) bool {
	switch underlying := typ.Underlying().(type) {
	case *types.Struct:
		for field := range underlying.Fields() {
			if field.Embedded() {
				return true
			}
		}
	case *types.Interface:
		return underlying.NumEmbeddeds() != 0
	}

	return false
}

// An unexported embedded type can still expose public methods. Record the
// effective value and pointer method sets, including promotion and shadowing.
func (f apiFormatter) recordMethodSets(result surface, name string, typ types.Type) {
	for _, receiver := range []types.Type{typ, types.NewPointer(typ)} {
		prefix := ""
		if _, ok := receiver.(*types.Pointer); ok {
			prefix = "*"
		}

		for method := range types.NewMethodSet(receiver).Methods() {
			if method.Obj().Exported() {
				result["methodset ("+prefix+name+")."+method.Obj().Name()] = f.typeText(method.Type())
			}
		}
	}
}

func (f apiFormatter) recordFieldSet(result surface, name string, typ types.Type, includeDirect bool) {
	names := make(map[string]bool)
	collectFieldNames(typ, make(map[types.Type]bool), names)

	for fieldName := range names {
		obj, index, _ := types.LookupFieldOrMethod(typ, true, nil, fieldName)

		field, ok := obj.(*types.Var)
		if !ok || !includeDirect && len(index) == 1 {
			continue // Ambiguous and shadowed promoted fields are inaccessible.
		}

		parent := typ

		var tag string

		for _, i := range index {
			structure := structOf(parent)
			tag = structure.Tag(i)
			parent = structure.Field(i).Type()
		}

		result["fieldset ("+name+")."+fieldName] = f.typeText(field.Type()) + " " + strconv.Quote(tag)
	}
}

func collectFieldNames(typ types.Type, visited map[types.Type]bool, names map[string]bool) {
	if visited[typ] {
		return
	}

	visited[typ] = true

	structure := structOf(typ)
	if structure == nil {
		return
	}

	for field := range structure.Fields() {
		if field.Exported() {
			names[field.Name()] = true
		}

		if field.Embedded() {
			collectFieldNames(field.Type(), visited, names)
		}
	}
}

func structOf(typ types.Type) *types.Struct {
	typ = types.Unalias(typ)
	if pointer, ok := typ.(*types.Pointer); ok {
		typ = pointer.Elem()
	}

	structure, _ := typ.Underlying().(*types.Struct)

	return structure
}

func (f apiFormatter) recordAlias(result surface, obj *types.TypeName) {
	typ := obj.Type()

	var generics string
	if alias, ok := typ.(*types.Alias); ok {
		generics = f.typeParamsText(alias.TypeParams())
		typ = alias.Rhs()
	}

	result["type "+obj.Name()] = "alias [" + generics + "] comparable=" + strconv.FormatBool(types.Comparable(typ)) + " " + f.typeText(typ)

	targetType := types.Unalias(typ)
	for {
		pointer, ok := targetType.(*types.Pointer)
		if !ok {
			break
		}

		targetType = types.Unalias(pointer.Elem())
	}

	if target, ok := targetType.(*types.Named); ok && !target.Obj().Exported() && target.Obj().Pkg() != nil && target.Obj().Pkg().Path() == f.packagePath {
		result["alias target "+obj.Name()] = f.typeText(target.Underlying())
	}

	f.recordMethodSets(result, obj.Name(), obj.Type())
	f.recordFieldSet(result, obj.Name(), obj.Type(), true)
}
