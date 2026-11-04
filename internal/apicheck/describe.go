package main

import (
	"go/types"
	"slices"
	"strconv"
	"strings"
)

type apiFormatter struct{ packagePath string }

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
	default:
		return types.TypeString(typ, func(pkg *types.Package) string {
			if pkg.Path() == f.packagePath {
				return ""
			}

			return pkg.Path()
		})
	}
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
		generics = append(generics, param.Obj().Name()+" "+f.typeText(param.Constraint()))
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
		members = append(members, f.typeText(embedded))
	}

	slices.Sort(members)

	return "interface { " + strings.Join(members, "; ") + " }"
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
}

func (f apiFormatter) recordAlias(result surface, obj *types.TypeName) {
	typ := obj.Type()

	var generics string
	if alias, ok := typ.(*types.Alias); ok {
		generics = f.typeParamsText(alias.TypeParams())
		typ = alias.Rhs()
	}

	result["type "+obj.Name()] = "alias [" + generics + "] comparable=" + strconv.FormatBool(types.Comparable(typ)) + " " + f.typeText(typ)
}
