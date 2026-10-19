package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const packageName = "github.com/nullmonk/enkodo"

// Used to find enkodo tags in the struct fields
var tag = regexp.MustCompile(`enkodo:"([^"]*)"`)
var (
	exitFunc    = os.Exit
	currentFile string
	currentPos  token.Pos
	currentFset *token.FileSet
)

// This is all the types we know about. If you need more, make a new TypeConverter.
// See Error type converter as an example
var enc_types_advanced = map[string]TypeConverter{
	"uint":    NewBasicTypeConverter("uint", "Uint"),
	"uint8":   NewBasicTypeConverter("uint8", "Uint8"),
	"byte":    NewBasicTypeConverter("byte", "Uint8"),
	"uint16":  NewBasicTypeConverter("uint16", "Uint16"),
	"uint32":  NewBasicTypeConverter("uint32", "Uint32"),
	"uint64":  NewBasicTypeConverter("uint64", "Uint64"),
	"int":     NewBasicTypeConverter("int", "Int"),
	"int8":    NewBasicTypeConverter("int8", "Int8"),
	"int16":   NewBasicTypeConverter("int16", "Int16"),
	"int32":   NewBasicTypeConverter("int32", "Int32"),
	"int64":   NewBasicTypeConverter("int64", "Int64"),
	"float32": NewBasicTypeConverter("float32", "Float32"),
	"float64": NewBasicTypeConverter("float64", "Float64"),
	"string":    NewBasicTypeConverter("string", "String"),
	"bool":      NewBasicTypeConverter("bool", "Bool"),
	"[]byte":    NewBasicTypeConverter("[]byte", "Bytes"),
	"error":     &ErrorTypeConverter{},
	"Time":      &TimeTypeConverter{},
	"time.Time": &TimeTypeConverter{},
}

const ident = "\t"

type TypeConverter interface {
	// Name of the golang type for this converter
	Name() string
	// Name of the enkodo function used to encode it.
	EnkodoFunction() string
	// Take the value (e.g. struct.field) and return and modifications
	// (e.g. struct.field.String()) to get passed to EnkodoFunction.
	// must match the INPUT type of the EnkodoFunction
	Enc(val string) string
	// This code take a value v (output from EnkodoFunction) and converts it to Name()
	//
	// return nothing to just use the raw value of EnkodoFunc (e.g. Name = "string"
	// and EnkodoFunc = "enkodo.String()")
	//
	// val, _ = enkodo.String()
	// struct.Field = CustomType(val)
	Dec(val string) string
	// These packages must be imported to use this advanced type, ensure are included at the top
	Imports() []string
}

type ErrorTypeConverter struct{}

func (e *ErrorTypeConverter) Name() string {
	return "error"
}

func (e *ErrorTypeConverter) EnkodoFunction() string {
	return "String"
}

func (e *ErrorTypeConverter) Enc(val string) string {
	return fmt.Sprintf("%s.Error()", val)
}

func (e *ErrorTypeConverter) Dec(val string) string {
	return fmt.Sprintf("errors.New(%s)", val)
}

func (e *ErrorTypeConverter) Imports() []string {
	return []string{"errors"}
}

type TimeTypeConverter struct{}

func (t *TimeTypeConverter) Name() string {
	return "time.Time"
}

func (t *TimeTypeConverter) EnkodoFunction() string {
	return "Int64"
}

func (t *TimeTypeConverter) Enc(val string) string {
	return fmt.Sprintf("%s.UnixNano()", val)
}

func (t *TimeTypeConverter) Dec(val string) string {
	return fmt.Sprintf("time.Unix(0, %s)", val)
}

func (t *TimeTypeConverter) Imports() []string {
	return []string{"time"}
}

type BasicTypeConverter struct {
	goName  string
	enkFunc string
}

func NewBasicTypeConverter(gotype, enkodoFunction string) *BasicTypeConverter {
	return &BasicTypeConverter{
		goName:  gotype,
		enkFunc: enkodoFunction,
	}
}

func (b *BasicTypeConverter) Name() string {
	return b.goName
}

func (b *BasicTypeConverter) EnkodoFunction() string {
	return b.enkFunc
}

func (b *BasicTypeConverter) Enc(val string) string {
	return val // Use as is
}

func (b *BasicTypeConverter) Dec(val string) string {
	return "" // Not mods needed, assumes enkFunc returns goName
}

func (b *BasicTypeConverter) Imports() []string {
	return nil // Does not need to import anything
}

// A field on a struct, has a field name, go type, and optional override type
type Field struct {
	Name         string
	Type         string
	OverrideType string
	EncodeFunc   string
	DecodeFunc   string
	Pos          token.Pos
}

// A struct has a name, and lots of fields
type Struct struct {
	Name   string
	Fields []Field
	Pos    token.Pos

	_declared   map[string]string
	_hasLoopVar bool
	simpleTypes map[string]string
}

func (s *Struct) String() string {
	return fmt.Sprintf("%s: %v", s.Name, s.Fields)
}

func (s *Struct) EncodeFunc(f io.Writer) error {
	if s.Pos.IsValid() {
		currentPos = s.Pos
	}
	s._declared = make(map[string]string)
	fnRef := strings.ToLower(s.Name[0:1])
	fmt.Fprintf(f, "func (%s *%s) MarshalEnkodo(enc *enkodo.Encoder) (err error) {\n", fnRef, s.Name)
	for _, field := range s.Fields {
		if field.Pos.IsValid() {
			currentPos = field.Pos
		}
		field.Name = fnRef + "." + field.Name
		s.EncodeField(1, field, f)
	}
	fmt.Fprintf(f, ident+"return\n}\n\n")
	return nil
}

func (s *Struct) DecodeFunc(f io.Writer) error {
	if s.Pos.IsValid() {
		currentPos = s.Pos
	}
	fnRef := strings.ToLower(s.Name[0:1])
	fmt.Fprintf(f, "func (%s *%s) UnmarshalEnkodo(dec *enkodo.Decoder) (err error) {\n", fnRef, s.Name)
	for _, field := range s.Fields {
		if field.Pos.IsValid() {
			currentPos = field.Pos
		}
		field.Name = fnRef + "." + field.Name
		s.DecodeField(1, field, f)
	}
	fmt.Fprint(f, ident+"return\n}\n\n")
	return nil
}

func (s *Struct) EncodeField(identCount int, field Field, f io.Writer) (err error) {
	if field.Pos.IsValid() {
		currentPos = field.Pos
	}
	dent := strings.Repeat(ident, identCount)
	name := field.Name
	rawName := field.Name
	if field.Type != "" && field.Type[0] != '*' && field.OverrideType == "" && s.simpleTypes != nil {
		if underlying, ok := s.simpleTypes[field.Type]; ok {
			field.OverrideType = underlying
		}
	}
	if field.OverrideType != "" {
		name = fmt.Sprintf("%s(%s)", field.OverrideType, field.Name)
		field.Type = field.OverrideType
	}

	if field.Type == "" || field.Type[0] == '[' && len(field.Type) == 2 {
		fmt.Fprintf(f, "%s// Do not know what to do with %s (%s)\n", dent, field.Name, field.Type)
		return
	}

	// Get the TypeConverter for this field type
	if conv, ok := enc_types_advanced[field.Type]; ok {
		val := conv.Enc(name)
		if field.EncodeFunc != "" {
			if strings.HasPrefix(field.EncodeFunc, ".") {
				val = rawName + field.EncodeFunc
			} else {
				val = fmt.Sprintf("%s(%s)", field.EncodeFunc, rawName)
			}
		}
		fmt.Fprintf(f, "%senc.%s(%s)\n", dent, conv.EnkodoFunction(), val)
		return
	}

	// Handle pointers to other types
	if field.Type[0] == '*' {
		elemType := field.Type[1:]
		if conv, ok := enc_types_advanced[elemType]; ok {
			fmt.Fprintf(f, "%sif %s != nil {\n", dent, name)
			fmt.Fprintf(f, "%s\tenc.Bool(true)\n", dent)
			fmt.Fprintf(f, "%s\tenc.%s(*%s)\n", dent, conv.EnkodoFunction(), name)
			fmt.Fprintf(f, "%s} else {\n", dent)
			fmt.Fprintf(f, "%s\tenc.Bool(false)\n", dent)
			fmt.Fprintf(f, "%s}\n", dent)
			return
		}
		if s.simpleTypes != nil {
			if underlying, ok := s.simpleTypes[elemType]; ok {
				if conv, ok := enc_types_advanced[underlying]; ok {
					fmt.Fprintf(f, "%sif %s != nil {\n", dent, name)
					fmt.Fprintf(f, "%s\tenc.Bool(true)\n", dent)
					fmt.Fprintf(f, "%s\tenc.%s(%s(*%s))\n", dent, conv.EnkodoFunction(), underlying, name)
					fmt.Fprintf(f, "%s} else {\n", dent)
					fmt.Fprintf(f, "%s\tenc.Bool(false)\n", dent)
					fmt.Fprintf(f, "%s}\n", dent)
					return
				}
			}
		}

		val := name
		if field.EncodeFunc != "" {
			if strings.HasPrefix(field.EncodeFunc, ".") {
				val = rawName + field.EncodeFunc
			} else {
				val = fmt.Sprintf("%s(%s)", field.EncodeFunc, rawName)
			}
		}
		fmt.Fprintf(f, "%sif %s != nil {\n", dent, name)
		fmt.Fprintf(f, "%s\tenc.Bool(true)\n", dent)
		fmt.Fprintf(f, "%s\tenc.Encode(%s)\n", dent, val)
		fmt.Fprintf(f, "%s} else {\n", dent)
		fmt.Fprintf(f, "%s\tenc.Bool(false)\n", dent)
		fmt.Fprintf(f, "%s}\n", dent)
		return
	}

	// Handle maps (e.g. map[string]string)
	if strings.HasPrefix(field.Type, "map[string]string") {
		fmt.Fprintf(f, "%senc.Int(len(%s))\n", dent, name)
		fmt.Fprintf(f, "%sfor k, v := range %s {\n", dent, name)
		fmt.Fprintf(f, "%s\tenc.String(k)\n", dent)
		fmt.Fprintf(f, "%s\tenc.String(v)\n", dent)
		fmt.Fprintln(f, dent+"}")
		return
	}

	// Handle arrays
	if field.Type[0] == '[' {
		fmt.Fprintf(f, "%senc.Int(len(%s))\n", dent, name)
		fmt.Fprintf(f, "%sfor _, v := range %s {\n", dent, name)
		elemType := field.Type[2:]
		vName := "v"
		isSimple := false
		if _, ok := enc_types_advanced[elemType]; ok {
			isSimple = true
		} else if s.simpleTypes != nil {
			if _, ok := s.simpleTypes[elemType]; ok {
				isSimple = true
			}
		}
		if !isSimple && unicode.IsUpper(rune(elemType[0])) {
			vName = "&v"
		}
		if err := s.EncodeField(identCount+1, Field{Name: vName, Type: elemType, Pos: field.Pos}, f); err != nil {
			return err
		}
		fmt.Fprintln(f, dent+"}")
		return
	}

	// Handle struct implementing Encodee by pointer or value
	lastDot := strings.LastIndex(field.Type, ".")
	typeBase := field.Type
	if lastDot >= 0 && lastDot+1 < len(field.Type) {
		typeBase = field.Type[lastDot+1:]
	}
	if unicode.IsUpper(rune(typeBase[0])) {
		if strings.HasPrefix(name, "&") {
			fmt.Fprintf(f, "%senc.Encode(%s)\n", dent, name)
		} else {
			fmt.Fprintf(f, "%senc.Encode(&%s)\n", dent, name)
		}
		return
	}

	fmt.Fprintf(f, "%s// Do not know what to do with %s (%s)\n", dent, field.Name, field.Type)
	return nil
}

func (s *Struct) DecodeField(identCount int, field Field, f io.Writer) (err error) {
	if field.Pos.IsValid() {
		currentPos = field.Pos
	}
	dent := strings.Repeat(ident, identCount)
	name := field.Name
	/*
		var ogType string
		if field.OverrideType != "" {
			ogType = field.Type
			field.Type = field.OverrideType
		}
	*/
	if field.Type == "" || field.Type[0] == '[' && len(field.Type) == 2 {
		fmt.Fprintf(f, "%s// Do not know what to do with %s (%s)\n", dent, field.Name, field.Type)
		return
	}
	// bytes is a special case for decode because we need to build the array
	if field.Type == "[]byte" {
		fmt.Fprintf(f, "%s%s = make([]byte, 0)\n", dent, name)
		fmt.Fprintf(f, "%sif err = dec.Bytes(&%s); err != nil {\n", dent, name)
		fmt.Fprintf(f, "%sreturn\n%s}\n", dent+ident, dent)
		return
	}

	// These basic functions are all error wrapped
	if field.Type != "" && field.Type[0] != '*' && field.OverrideType == "" && s.simpleTypes != nil {
		if underlying, ok := s.simpleTypes[field.Type]; ok {
			field.OverrideType = underlying
		}
	}
	typ := field.Type
	if field.OverrideType != "" {
		typ = field.OverrideType
	}

	if conv, ok := enc_types_advanced[typ]; ok {
		// Special case for overrides where we assign it to a different value, then set it in the obj
		//init, varName := initType(field.Type)
		//enhanced decoding where its converted
		d := conv.Dec("v")

		if field.DecodeFunc != "" {
			if strings.HasPrefix(field.DecodeFunc, ".") {
				d = fmt.Sprintf("%s%s(v)", field.Name, field.DecodeFunc)
			} else {
				d = fmt.Sprintf("%s(v)", field.DecodeFunc)
			}
		} else if field.OverrideType != "" {
			// Override requires a typecast back to the original gotype
			if d == "" {
				d = "v"
			}
			d = fmt.Sprintf("%s(%s)", field.Type, d)
		}

		if d != "" {
			/* Should come out like this:
			// assume .Field error
			if v, err := dec.String(); err == nil {
				struct.Field = errors.New(v)
			} else {
				return err
			}
			*/

			fmt.Fprintf(f, "%sif v, err := dec.%s(); err == nil {\n", dent, conv.EnkodoFunction())
			if field.DecodeFunc != "" && strings.HasPrefix(field.DecodeFunc, ".") {
				fmt.Fprintf(f, "%s%s\n", dent+ident, d)
			} else {
				fmt.Fprintf(f, "%s%s = %s\n", dent+ident, field.Name, d)
			}
			fmt.Fprintf(f, "%s} else {\n", dent)
			fmt.Fprintf(f, "%sreturn err\n", dent+ident)
			fmt.Fprintf(f, "%s}\n", dent)
			//fmt.Fprintf(f, "%s%s = %s(%s)\n", dent, name, ogType, varName)
		} else {

			fmt.Fprintf(f, "%sif %s, err = dec.%s(); err != nil {\n", dent, field.Name, conv.EnkodoFunction())
			fmt.Fprintf(f, "%sreturn err\n", dent+ident)
			fmt.Fprintf(f, "%s}\n", dent)
		}
		return
	}

	// Handle pointers to other types
	if field.Type[0] == '*' {
		elemType := strings.TrimPrefix(field.Type, "*")
		if conv, ok := enc_types_advanced[elemType]; ok {
			fmt.Fprintf(f, "%sif _hasVal, err := dec.Bool(); err != nil {\n", dent)
			fmt.Fprintf(f, "%s\treturn err\n", dent)
			fmt.Fprintf(f, "%s} else if _hasVal {\n", dent)
			fmt.Fprintf(f, "%s\tif v, err := dec.%s(); err == nil {\n", dent, conv.EnkodoFunction())
			fmt.Fprintf(f, "%s\t\t%s = &v\n", dent, name)
			fmt.Fprintf(f, "%s\t} else {\n", dent)
			fmt.Fprintf(f, "%s\t\treturn err\n", dent)
			fmt.Fprintf(f, "%s\t}\n", dent)
			fmt.Fprintf(f, "%s}\n", dent)
			return
		}
		if s.simpleTypes != nil {
			if underlying, ok := s.simpleTypes[elemType]; ok {
				if conv, ok := enc_types_advanced[underlying]; ok {
					fmt.Fprintf(f, "%sif _hasVal, err := dec.Bool(); err != nil {\n", dent)
					fmt.Fprintf(f, "%s\treturn err\n", dent)
					fmt.Fprintf(f, "%s} else if _hasVal {\n", dent)
					fmt.Fprintf(f, "%s\tif v, err := dec.%s(); err == nil {\n", dent, conv.EnkodoFunction())
					fmt.Fprintf(f, "%s\t\ttmp := %s(v)\n", dent, elemType)
					fmt.Fprintf(f, "%s\t\t%s = &tmp\n", dent, name)
					fmt.Fprintf(f, "%s\t} else {\n", dent)
					fmt.Fprintf(f, "%s\t\treturn err\n", dent)
					fmt.Fprintf(f, "%s\t}\n", dent)
					fmt.Fprintf(f, "%s}\n", dent)
					return
				}
			}
		}

		fmt.Fprintf(f, "%sif _hasVal, err := dec.Bool(); err != nil {\n", dent)
		fmt.Fprintf(f, "%s\treturn err\n", dent)
		fmt.Fprintf(f, "%s} else if _hasVal {\n", dent)
		fmt.Fprintf(f, "%s\t%s = new(%s)\n", dent, name, strings.Trim(field.Type, "*"))
		fmt.Fprintf(f, "%s\tif err = dec.Decode(%s); err != nil {\n", dent, name)
		fmt.Fprintf(f, "%s\t\treturn err\n", dent)
		fmt.Fprintf(f, "%s\t}\n", dent)
		fmt.Fprintf(f, "%s}\n", dent)
		return
	}

	// Handle maps (e.g. map[string]string)
	if strings.HasPrefix(field.Type, "map[string]string") {
		varMapLen := "_mapLen"
		if _, ok := s._declared[varMapLen]; !ok {
			s._declared[varMapLen] = "int"
			fmt.Fprintf(f, "%svar %s int\n", dent, varMapLen)
		}
		fmt.Fprintf(f, "%sif %s, err = dec.Int(); err != nil {\n", dent, varMapLen)
		fmt.Fprintf(f, "%s\treturn err\n", dent)
		fmt.Fprintf(f, "%s}\n", dent)
		fmt.Fprintf(f, "%s%s = make(map[string]string, %s)\n", dent, name, varMapLen)
		fmt.Fprintf(f, "%sfor i := 0; i < %s; i++ {\n", dent, varMapLen)
		fmt.Fprintf(f, "%s\tk, err := dec.String()\n", dent)
		fmt.Fprintf(f, "%s\tif err != nil {\n", dent)
		fmt.Fprintf(f, "%s\t\treturn err\n", dent)
		fmt.Fprintf(f, "%s\t}\n", dent)
		fmt.Fprintf(f, "%s\tv, err := dec.String()\n", dent)
		fmt.Fprintf(f, "%s\tif err != nil {\n", dent)
		fmt.Fprintf(f, "%s\t\treturn err\n", dent)
		fmt.Fprintf(f, "%s\t}\n", dent)
		fmt.Fprintf(f, "%s\t%s[k] = v\n", dent, name)
		fmt.Fprintln(f, dent+"}")
		return
	}

	// Handle arrays
	if field.Type[0] == '[' {
		// Make sure we have this loop var initialized
		if _, ok := s._declared["_arrLen"]; !ok {
			s._declared["_arrLen"] = "int"
			fmt.Fprintf(f, "%svar _arrLen int\n", dent)
		}
		// temp var for the type
		init, temp := initType(field.Type)
		// Read the len
		s.DecodeField(identCount, Field{Name: "_arrLen", Type: "int", Pos: field.Pos}, f)
		// Make the buffer
		fmt.Fprintf(f, "%s%s = make(%s, 0, _arrLen)\n", dent, name, field.Type)
		fmt.Fprintf(f, "%sfor i := 0; i < _arrLen; i++ {\n", dent)
		fmt.Fprintf(f, "%s%s\n", dent+ident, init)

		// This initType makes a var per type in a loop, its technically not needed as we
		// could use a temp var, but
		if err := s.DecodeField(identCount+1, Field{Name: temp, Type: field.Type[2:], Pos: field.Pos}, f); err != nil {
			return err
		}
		fmt.Fprintf(f, "%s%s = append(%s, %s)\n", dent+ident, name, name, temp)
		fmt.Fprintln(f, dent+"}")
		return
	}

	// Handle struct implementing Decodee by pointer
	lastDot := strings.LastIndex(field.Type, ".")
	typeBase := field.Type
	if lastDot >= 0 && lastDot+1 < len(field.Type) {
		typeBase = field.Type[lastDot+1:]
	}
	if unicode.IsUpper(rune(typeBase[0])) {
		fmt.Fprintf(f, "%sif err = dec.Decode(&%s); err != nil {\n", dent, name)
		fmt.Fprintf(f, "%sreturn\n%s}\n", dent+ident, dent)
		return
	}

	return nil
}

/*
	Each var that is appended to an array needs to be intialized, and have a unique name per type.

This function determines how to handle that properly
*/
func initType(typ string) (init string, name string) {
	clean_typ := strings.Trim(typ, "[]")
	name = "t"
	//name = "_" + strings.ToLower(strings.TrimLeft(clean_typ, "*"))
	if typ[0] == '*' {
		init = fmt.Sprintf("var %s = new(%s)", name, clean_typ)
	} else {
		init = fmt.Sprintf("var %s %s", name, clean_typ)
	}
	return
}

func GetFieldType(f ast.Expr) (result string) {
	if f != nil && f.Pos().IsValid() {
		currentPos = f.Pos()
	}
	switch t := f.(type) {
	case *ast.Ident:
		// basic types (e.g. Int)
		result = t.Name
	case *ast.StarExpr:
		// pointer types
		inner := GetFieldType(t.X)
		if inner != "" {
			result = "*" + inner
		}
	case *ast.ArrayType:
		result = "[]" + GetFieldType(t.Elt)
	case *ast.MapType:
		result = "map[" + GetFieldType(t.Key) + "]" + GetFieldType(t.Value)
	case *ast.SelectorExpr:
		if xIdent, ok := t.X.(*ast.Ident); ok {
			result = xIdent.Name + "." + t.Sel.Name
		} else {
			result = t.Sel.Name
		}
	default:
		// uncomment below to error and see new types
		// result = f.(*ast.Ident).Name
		return
	}
	return
}

func getEmbeddedFieldName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return getEmbeddedFieldName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return getEmbeddedFieldName(t.X)
	case *ast.IndexListExpr:
		return getEmbeddedFieldName(t.X)
	default:
		return ""
	}
}

func GetStructFields(obj *ast.Object) *Struct {
	if obj.Decl == nil {
		return nil
	}
	if node, ok := obj.Decl.(ast.Node); ok && node.Pos().IsValid() {
		currentPos = node.Pos()
	}

	ts, ok := obj.Decl.(*ast.TypeSpec)
	if !ok {
		return nil // not a type definition
	}
	if ts.Pos().IsValid() {
		currentPos = ts.Pos()
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil // not a struct
	}

	s := &Struct{
		Name:   ts.Name.Name,
		Fields: make([]Field, 0),
		Pos:    ts.Pos(),
	}

	for _, field := range st.Fields.List {
		if field.Pos().IsValid() {
			currentPos = field.Pos()
		}

		// skip fields that dont have the enkodo tag
		if field.Tag == nil || !strings.Contains(field.Tag.Value, "enkodo") {
			continue
		}

		name := ""
		if len(field.Names) > 0 && field.Names[0] != nil {
			name = field.Names[0].Name
		} else {
			name = getEmbeddedFieldName(field.Type)
		}

		if name == "" || !unicode.IsUpper(rune(name[0])) {
			// Only handle exported variables for now
			continue
		}

		f := Field{
			Name: name,
			Type: GetFieldType(field.Type),
			Pos:  field.Pos(),
		}
		// Override the type with anything in a struct tag. E.g. enkodo:"int"
		match := tag.FindStringSubmatch(field.Tag.Value)
		if len(match) > 1 {
			parts := strings.Split(match[1], ",")
			if len(parts) == 1 && strings.HasPrefix(parts[0], ".") {
				f.EncodeFunc = parts[0]
			} else {
				if len(parts) > 0 && parts[0] != "" {
					f.OverrideType = parts[0]
				}
				if len(parts) > 1 && parts[1] != "" {
					f.EncodeFunc = parts[1]
				}
				if len(parts) > 2 && parts[2] != "" {
					f.DecodeFunc = parts[2]
				}
			}
		}
		if f.OverrideType == "panic_test" {
			panic("simulated panic for testing")
		}
		if f.Type == "" && f.OverrideType == "" {
			continue
		}
		s.Fields = append(s.Fields, f)
	}
	if len(s.Fields) > 0 {
		return s
	}
	return nil
}

func collectTypesFromAST(fil *ast.File, rawTypes map[string]string) {
	for _, decl := range fil.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.TYPE {
			continue
		}
		for _, spec := range genDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			underlying := GetFieldType(typeSpec.Type)
			if underlying != "" {
				rawTypes[typeSpec.Name.Name] = underlying
			}
		}
	}
}

func getPackageSimpleTypes(fset *token.FileSet, file string, fil *ast.File) map[string]string {
	rawTypes := make(map[string]string)
	collectTypesFromAST(fil, rawTypes)

	dir := filepath.Dir(file)
	if entries, err := os.ReadDir(dir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_enkodo.go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			otherPath := filepath.Join(dir, name)
			if otherPath == file {
				continue
			}
			otherFil, err := parser.ParseFile(fset, otherPath, nil, 0)
			if err != nil || otherFil.Name == nil || otherFil.Name.Name != fil.Name.Name {
				continue
			}
			collectTypesFromAST(otherFil, rawTypes)
		}
	}

	simpleTypes := make(map[string]string)
	for name, underlying := range rawTypes {
		visited := map[string]bool{name: true}
		cur := underlying
		for {
			if _, ok := enc_types_advanced[cur]; ok {
				simpleTypes[name] = cur
				break
			}
			next, exists := rawTypes[cur]
			if !exists || visited[next] {
				break
			}
			visited[next] = true
			cur = next
		}
	}
	return simpleTypes
}

func isStdLib(path string) bool {
	if bp, err := build.Import(path, "", build.FindOnly); err == nil && bp.Goroot {
		return true
	}
	firstPart := strings.Split(path, "/")[0]
	return !strings.Contains(firstPart, ".")
}

func objectsInFile(file string) (err error) {
	currentFile = file
	currentPos = token.NoPos
	currentFset = nil

	var createdFile string
	defer func() {
		if r := recover(); r != nil {
			if createdFile != "" {
				_ = os.Remove(createdFile)
			}
			filename := file
			line := 0
			if currentFset != nil && currentPos.IsValid() {
				pos := currentFset.Position(currentPos)
				if pos.Filename != "" {
					filename = pos.Filename
				}
				line = pos.Line
			}
			if line > 0 {
				fmt.Fprintf(os.Stderr, "%s:%d: panic: %v\n", filename, line, r)
				err = fmt.Errorf("%s:%d: panic: %v", filename, line, r)
			} else {
				fmt.Fprintf(os.Stderr, "%s: panic: %v\n", filename, r)
				err = fmt.Errorf("%s: panic: %v", filename, r)
			}
			exitFunc(1)
		}
	}()

	fset := token.NewFileSet()
	currentFset = fset
	fil, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		log.Fatalf("failed to parse %s: %s", file, err)
	}

	pkg := fil.Name.Name // package name

	simpleTypes := getPackageSimpleTypes(fset, file, fil)

	structs := make([]*Struct, 0)
	for _, obj := range fil.Scope.Objects {
		if obj.Decl == nil {
			continue
		}
		if node, ok := obj.Decl.(ast.Node); ok && node.Pos().IsValid() {
			currentPos = node.Pos()
		}

		s := GetStructFields(obj)
		if s == nil {
			continue
		}
		s.simpleTypes = simpleTypes
		structs = append(structs, s)
	}

	sort.Slice(structs, func(i, j int) bool {
		if structs[i].Pos != structs[j].Pos {
			return structs[i].Pos < structs[j].Pos
		}
		return structs[i].Name < structs[j].Name
	})

	if len(structs) == 0 {
		return nil
	}
	// open the output file
	var out io.Writer
	if len(os.Args) > 2 && os.Args[2] == "-" {
		out = os.Stdout
	} else {
		filename := file[:len(file)-len(filepath.Ext(file))] + "_enkodo.go"
		fmt.Printf("Found %d enkodo structs in %s, saving to %s\n", len(structs), file, filename)
		oFile, err := os.Create(filename)
		if err != nil {
			return err
		}
		createdFile = filename
		defer oFile.Close()
		out = oFile
	}

	// By default we import enkodo
	imports := map[string]interface{}{
		packageName: true,
	}

	// Map of package aliases/names to import path from the source file
	sourceImports := make(map[string]string)
	for _, imp := range fil.Imports {
		if imp.Pos().IsValid() {
			currentPos = imp.Pos()
		}
		path := strings.Trim(imp.Path.Value, `"`)
		name := filepath.Base(path)
		if imp.Name != nil && imp.Name.Name != "" {
			name = imp.Name.Name
		}
		sourceImports[name] = path
	}

	var body bytes.Buffer
	for _, st := range structs {
		if st.Pos.IsValid() {
			currentPos = st.Pos
		}
		st.EncodeFunc(&body)
		st.DecodeFunc(&body)
	}
	bodyStr := body.String()

	// Check all the types that we will convert and see if they need to import anything
	for _, struc := range structs {
		if struc.Pos.IsValid() {
			currentPos = struc.Pos
		}
		for _, field := range struc.Fields {
			if field.Pos.IsValid() {
				currentPos = field.Pos
			}
			ty := field.Type
			if field.OverrideType != "" {
				ty = field.OverrideType
			}
			if conv, ok := enc_types_advanced[ty]; ok {
				for _, impt := range conv.Imports() {
					imports[impt] = true
				}
			}
		}
	}

	// Only import packages from source file that are actually referenced in the generated code (e.g. command.Command)
	for name, impPath := range sourceImports {
		if strings.Contains(bodyStr, name+".") {
			imports[impPath] = true
		}
	}

	var buf bytes.Buffer
	fmt.Fprint(&buf, "/* This file is auto-generated by enkodo */\n")
	fmt.Fprintf(&buf, "package %s\n\n", pkg)

	var stdlibImports []string
	var otherImports []string
	for imp := range imports {
		if isStdLib(imp) {
			stdlibImports = append(stdlibImports, imp)
		} else {
			otherImports = append(otherImports, imp)
		}
	}
	sort.Strings(stdlibImports)
	sort.Strings(otherImports)

	if len(stdlibImports)+len(otherImports) > 0 {
		fmt.Fprintln(&buf, "import (")
		for _, imp := range stdlibImports {
			fmt.Fprintf(&buf, "\t\"%s\"\n", imp)
		}
		if len(stdlibImports) > 0 && len(otherImports) > 0 {
			fmt.Fprintln(&buf, "")
		}
		for _, imp := range otherImports {
			fmt.Fprintf(&buf, "\t\"%s\"\n", imp)
		}
		fmt.Fprintln(&buf, ")")
		fmt.Fprintln(&buf, "")
	}

	buf.Write(body.Bytes())

	src := buf.Bytes()
	if formatted, err := format.Source(src); err == nil {
		src = formatted
	}

	_, err = out.Write(src)
	return err
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <path> [ - ]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Generate enkodo marshal/unmarshal functions for Go source files under the given path.")
		fmt.Fprintln(os.Stderr, "If the optional second positional argument is '-', generated files are written to stdout.")
		fmt.Fprintln(os.Stderr, "\nExamples:")
		fmt.Fprintf(os.Stderr, "  %s ./pkg\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s ./example/basic\n", os.Args[0])
		flag.PrintDefaults()
	}

	help := flag.Bool("help", false, "Show help")
	flag.Parse()

	// also accept GNU-style --help
	for _, a := range os.Args[1:] {
		if a == "--help" {
			*help = true
			break
		}
	}
	if *help {
		flag.Usage()
		os.Exit(0)
	}

	opath := flag.Arg(0)
	if opath == "" {
		flag.Usage()
		log.Fatal("No input path given")
	}

	files := make([]string, 0, 10)

	err := filepath.WalkDir(opath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_enkodo.go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}

	if len(files) == 0 {
		log.Fatal("No input files given")
	}
	for _, file := range files {
		if err := objectsInFile(file); err != nil {
			exitFunc(1)
		}
	}
}
