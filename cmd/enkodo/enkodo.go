package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const packageName = "github.com/nullmonk/enkodo"

// Used to find enkodo tags in the struct fields
var tag = regexp.MustCompile("enkodo:\"(\\w+)\"")

// This is all the types we know about. If you need more, make a new TypeConverter.
// See Error type converter as an example
var enc_types_advanced = map[string]TypeConverter{
	"uint":    NewBasicTypeConverter("uint", "Uint"),
	"uint8":   NewBasicTypeConverter("uint8", "Uint8"),
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
	"string":  NewBasicTypeConverter("string", "String"),
	"bool":    NewBasicTypeConverter("bool", "Bool"),
	"[]byte":  NewBasicTypeConverter("[]byte", "Bytes"),
	"error":   &ErrorTypeConverter{},
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
}

// A struct has a name, and lots of fields
type Struct struct {
	Name   string
	Fields []Field

	_declared   map[string]string
	_hasLoopVar bool
}

func (s *Struct) String() string {
	return fmt.Sprintf("%s: %v", s.Name, s.Fields)
}

func (s *Struct) EncodeFunc(f io.Writer) error {
	s._declared = make(map[string]string)
	fnRef := strings.ToLower(s.Name[0:1])
	fmt.Fprintf(f, "func (%s *%s) MarshalEnkodo(enc *enkodo.Encoder) (err error) {\n", fnRef, s.Name)
	for _, field := range s.Fields {
		field.Name = fnRef + "." + field.Name
		s.EncodeField(1, field, f)
	}
	fmt.Fprintf(f, ident+"return\n}\n\n")
	return nil
}

func (s *Struct) DecodeFunc(f io.Writer) error {
	fnRef := strings.ToLower(s.Name[0:1])
	fmt.Fprintf(f, "func (%s *%s) UnmarshalEnkodo(dec *enkodo.Decoder) (err error) {\n", fnRef, s.Name)
	for _, field := range s.Fields {
		field.Name = fnRef + "." + field.Name
		s.DecodeField(1, field, f)
	}
	fmt.Fprint(f, ident+"return\n}\n\n")
	return nil
}

func (s *Struct) EncodeField(identCount int, field Field, f io.Writer) (err error) {
	dent := strings.Repeat(ident, identCount)
	name := field.Name
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
		fmt.Fprintf(f, "%senc.%s(%s)\n", dent, conv.EnkodoFunction(), conv.Enc(name))
		return
	}

	// Handle pointers to other types
	if field.Type[0] == '*' {
		fmt.Fprintf(f, "%senc.Encode(%s)\n", dent, name)
		return
	}

	// Handle arrays
	if field.Type[0] == '[' {
		fmt.Fprintf(f, "%senc.Int(len(%s))\n", dent, name)
		fmt.Fprintf(f, "%sfor _, v := range %s {\n", dent, name)
		if err := s.EncodeField(identCount+1, Field{Name: "v", Type: field.Type[2:]}, f); err != nil {
			return err
		}
		fmt.Fprintln(f, dent+"}")
		return
	}

	fmt.Fprintf(f, "%s// Do not know what to do with %s (%s)\n", dent, field.Name, field.Type)
	return nil
}

func (s *Struct) DecodeField(identCount int, field Field, f io.Writer) (err error) {
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
	typ := field.Type
	if field.OverrideType != "" {
		typ = field.OverrideType
	}

	if conv, ok := enc_types_advanced[typ]; ok {
		// Special case for overrides where we assign it to a different value, then set it in the obj
		//init, varName := initType(field.Type)
		//enhanced decoding where its converted
		d := conv.Dec("v")
		// Override requires a typecast back to the original gotype
		if field.OverrideType != "" {
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
			fmt.Fprintf(f, "%s%s = %s\n", dent+ident, field.Name, d)
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
		fmt.Fprintf(f, "%s%s = new(%s)\n", dent, name, strings.Trim(field.Type, "*"))
		fmt.Fprintf(f, "%sif err = dec.Decode(%s); err != nil {\n", dent, name)
		fmt.Fprintf(f, "%sreturn\n%s}\n", dent+ident, dent)
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
		s.DecodeField(identCount, Field{"_arrLen", "int", ""}, f)
		// Make the buffer
		fmt.Fprintf(f, "%s%s = make(%s, 0, _arrLen)\n", dent, name, field.Type)
		fmt.Fprintf(f, "%sfor i := 0; i < _arrLen; i++ {\n", dent)
		fmt.Fprintf(f, "%s%s\n", dent+ident, init)

		// This initType makes a var per type in a loop, its technically not needed as we
		// could use a temp var, but
		if err := s.DecodeField(identCount+1, Field{temp, field.Type[2:], ""}, f); err != nil {
			return err
		}
		fmt.Fprintf(f, "%s%s = append(%s, %s)\n", dent+ident, name, name, temp)
		fmt.Fprintln(f, dent+"}")
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
	switch t := f.(type) {
	case *ast.Ident:
		// basic types (e.g. Int)
		result = t.Name
	case *ast.StarExpr:
		// pointer types
		if v, ok := t.X.(*ast.Ident); !ok {
			return
		} else {
			result = "*" + v.Name
		}
	case *ast.ArrayType:
		result = "[]" + GetFieldType(t.Elt)
	case *ast.SelectorExpr:
		result = t.Sel.Name
	default:
		// uncomment below to error and see new types
		// result = f.(*ast.Ident).Name
		return
	}
	return
}

func GetStructFields(obj *ast.Object) *Struct {
	if obj.Decl == nil {
		return nil
	}

	ts, ok := obj.Decl.(*ast.TypeSpec)
	if !ok {
		return nil // not a type definition
	}
	st, ok := ts.Type.(*ast.StructType)
	if !ok {
		return nil // not a struct
	}

	s := &Struct{
		Name:   ts.Name.Name,
		Fields: make([]Field, 0),
	}

	for _, field := range st.Fields.List {
		f := Field{
			Name: field.Names[0].Name,
			Type: GetFieldType(field.Type),
		}
		// Override the type with anything in a struct tag. E.g. enkodo:"int"
		// skip fields that dont have the enkodo tag
		if field.Tag == nil || !strings.Contains(field.Tag.Value, "enkodo") {
			continue
		}
		match := tag.FindStringSubmatch(field.Tag.Value)
		if len(match) > 1 && len(match[1]) > 1 {
			f.OverrideType = match[1]
		}
		if !unicode.IsUpper(rune(f.Name[0])) || (f.Type == "" && f.OverrideType == "") {
			// Only handle exported variables for now
			continue
		}
		s.Fields = append(s.Fields, f)
	}
	if len(s.Fields) > 0 {
		return s
	}
	return nil
}
func objectsInFile(file string) error {
	fset := token.NewFileSet()
	fil, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		log.Fatalf("failed to parse %s: %s", file, err)
	}

	pkg := fil.Name.Name // package name

	structs := make([]*Struct, 0)
	for _, obj := range fil.Scope.Objects {
		if obj.Decl == nil {
			continue
		}

		s := GetStructFields(obj)
		if s == nil {
			continue
		}
		structs = append(structs, s)
	}

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
		defer oFile.Close()
		out = oFile
	}

	// By default we import enkodo
	imports := map[string]interface{}{
		packageName: true,
	}
	// Check all the types that we will convert and see if they need to import anything
	for _, struc := range structs {
		for _, field := range struc.Fields {
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

	fmt.Fprint(out, "/* This file is auto-generated by enkodo */\n")
	fmt.Fprintf(out, "package %s\n\n", pkg)
	for i := range imports {
		fmt.Fprintf(out, "import \"%s\"\n", i)
	}
	fmt.Fprintln(out, "")

	for _, st := range structs {
		st.EncodeFunc(out)
		st.DecodeFunc(out)
	}
	return nil
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

	filepath.WalkDir(opath, func(path string, d fs.DirEntry, err error) error {
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})

	if len(files) == 0 {
		log.Fatal("No input files given")
	}
	for _, file := range files {
		objectsInFile(file)
	}
}
