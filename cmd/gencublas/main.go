// Copyright ©2016 The Gonum Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// generate_blas creates a blas.go file from the provided C header file
// with optionally added documentation from the documentation package.
package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"io/ioutil"
	"log"
	"os"
	"path"
	"strings"
	"text/template"

	"github.com/cznic/cc"
)

var (
	target        string // blas.go
	documentation string
	targetHeader  string
)

const (
	typ     = "impl *Standalone"
	header  = "cublasgen.h"
	prefix  = "cublas"
	warning = "Float32 implementations are autogenerated and not directly tested."
)

func init() {
	gopath := os.Getenv("GOPATH")
	cublasLoc := path.Join(gopath, "src/github.com/chewxy/cu/cublas")
	gonumLoc := path.Join(gopath, "src/github.com/gonum/blas")
	documentation = path.Join(gonumLoc, "/native")
	target = path.Join(cublasLoc, "blas.go")
	targetHeader = path.Join(cublasLoc, "batch.h")

}

const (
	cribDocs      = true
	elideRepeat   = true
	noteOrigin    = true
	separateFuncs = false
)

var skip = map[string]bool{
	"cublasErrprn":    true,
	"cublasSrotg":     true,
	"cublasSrotmg":    true,
	"cublasSrotm":     true,
	"cublasDrotg":     true,
	"cublasDrotmg":    true,
	"cublasDrotm":     true,
	"cublasCrotg":     true,
	"cublasZrotg":     true,
	"cublasCdotu_sub": true,
	"cublasCdotc_sub": true,
	"cublasZdotu_sub": true,
	"cublasZdotc_sub": true,

	// ATLAS extensions.
	"cublasCsrot": true,
	"cublasZdrot": true,

	// trmm
	"cublasStrmm": true,
	"cublasDtrmm": true,
	"cublasZtrmm": true,
	"cublasCtrmm": true,
}

var cToGoType = map[string]string{
	"int":    "int",
	"float":  "float32",
	"double": "float64",
}

var blasEnums = map[string]*template.Template{
	"CUBLAS_ORDER":     template.Must(template.New("order").Parse("order")),
	"CUBLAS_DIAG":      template.Must(template.New("diag").Parse("blas.Diag")),
	"CUBLAS_TRANSPOSE": template.Must(template.New("trans").Parse("blas.Transpose")),
	"CUBLAS_UPLO":      template.Must(template.New("uplo").Parse("blas.Uplo")),
	"CUBLAS_SIDE":      template.Must(template.New("side").Parse("blas.Side")),
}

var cgoEnums = map[string]*template.Template{
	"CUBLAS_ORDER":     template.Must(template.New("order").Parse("C.enum_CBLAS_ORDER(rowMajor)")),
	"CUBLAS_DIAG":      template.Must(template.New("diag").Parse("diag2cublasDiag({{.}})")),
	"CUBLAS_TRANSPOSE": template.Must(template.New("trans").Parse("trans2cublasTrans({{.}})")),
	"CUBLAS_UPLO":      template.Must(template.New("uplo").Parse("uplo2cublasUplo({{.}})")),
	"CUBLAS_SIDE":      template.Must(template.New("side").Parse("side2cublasSide({{.}})")),
}

var (
	complex64Type = map[TypeKey]*template.Template{
		{Kind: cc.FloatComplex, IsPointer: true}: template.Must(template.New("void*").Parse(
			`{{if eq . "alpha" "beta"}}complex64{{else}}[]complex64{{end}}`,
		))}

	complex128Type = map[TypeKey]*template.Template{
		{Kind: cc.DoubleComplex, IsPointer: true}: template.Must(template.New("void*").Parse(
			`{{if eq . "alpha" "beta"}}complex128{{else}}[]complex128{{end}}`,
		))}
)

var names = map[string]string{
	"uplo":   "ul",
	"trans":  "t",
	"transA": "tA",
	"transB": "tB",
	"side":   "s",
	"diag":   "d",
}

func shorten(n string) string {
	s, ok := names[n]
	if ok {
		return s
	}
	return n
}

func cblasTocublas(name string) string {
	retVal := strings.TrimPrefix(name, prefix)
	return fmt.Sprintf("cublas%s", strings.Title(retVal))
}

func main() {
	decls, err := Declarations(header)
	if err != nil {
		log.Fatal(err)
	}
	var docs map[string]map[string][]*ast.Comment
	if cribDocs {
		docs, err = DocComments(documentation)
		if err != nil {
			log.Fatal(err)
		}
	}
	var buf bytes.Buffer

	if err := handwritten.Execute(&buf, header); err != nil {
		log.Fatal(err)
	}

	var n int
	var writtenDecl []Declaration
	for _, d := range decls {
		if !strings.HasPrefix(d.Name, prefix) || skip[d.Name] {
			continue
		}

		if n != 0 && (separateFuncs || cribDocs) {
			buf.WriteByte('\n')
		}
		n++
		goSignature(&buf, d, docs["Implementation"])
		if noteOrigin {
			fmt.Fprintf(&buf, "\t// declared at %s %s %s ...\n", d.Position(), d.Return, d.Name)
		}
		buf.WriteString(` if impl.e != nil {
			return
		}

		`)
		parameterChecks(&buf, d, parameterCheckRules)
		buf.WriteByte('\t')
		cgoCall(&buf, d)
		buf.WriteString("}\n")

		writtenDecl = append(writtenDecl, d)
	}

	// write blas.go
	b, err := format.Source(buf.Bytes())
	if err != nil {
		log.Fatal(err)
	}
	err = ioutil.WriteFile(target, b, 0664)
	if err != nil {
		log.Fatal(err)
	}

	// write cublas.h
	f, err := os.Create(targetHeader)
	if err != nil {
		log.Fatal(err)
	}
	batchedCHeader.Execute(f, writtenDecl)
	f.Close()

}

func goSignature(buf *bytes.Buffer, d Declaration, docs map[string][]*ast.Comment) {
	blasName := strings.TrimPrefix(d.Name, prefix)
	goName := UpperCaseFirst(blasName)

	if docs != nil {
		if doc, ok := docs[goName]; ok {
			if strings.Contains(doc[len(doc)-1].Text, warning) {
				doc = doc[:len(doc)-2]
			}
			for _, c := range doc {
				buf.WriteString(c.Text)
				buf.WriteByte('\n')
			}
		}
	}

	parameters := d.Parameters()

	var voidPtrType map[TypeKey]*template.Template
	for _, p := range parameters {
		if p.Kind() == cc.Ptr && p.Elem().Kind() == cc.FloatComplex {
			switch {
			case blasName[0] == 'C', blasName[1] == 'C' && blasName[0] != 'Z':
				voidPtrType = complex64Type
			case blasName[0] == 'Z', blasName[1] == 'Z':
				voidPtrType = complex128Type
			}
			break
		}
	}

	fmt.Fprintf(buf, "func (%s) %s(", typ, goName)
	var retType string
	var hasRet bool
	c := 0
	for i, p := range parameters {
		if p.Kind() == cc.Enum && GoTypeForEnum(p.Type(), "", blasEnums) == "order" {
			continue
		}
		if p.Name() == "handle" {
			continue
		}
		if p.Name() == "result" {
			switch {
			case p.Kind() == cc.Enum:
				retType = GoTypeForEnum(p.Type(), "retVal", blasEnums)
			default:
				retType = GoTypeFor(p.Type(), "retVal", voidPtrType)
			}
			hasRet = true
			continue
		}

		if c != 0 {
			buf.WriteString(", ")
		}
		c++

		n := shorten(LowerCaseFirst(p.Name()))

		var this, next string
		switch {
		case p.Type().String() == "const int*":
			this = "int" // CUBLAS takes const int* for many things where it'd be an int in a normal blas call
		case p.Kind() == cc.Enum:
			this = GoTypeForEnum(p.Type(), n, blasEnums)
		default:
			this = GoTypeFor(p.Type(), n, voidPtrType)
		}

		if elideRepeat && i < len(parameters)-1 && p.Type().Kind() == parameters[i+1].Type().Kind() {
			p := parameters[i+1]
			n := shorten(LowerCaseFirst(p.Name()))
			switch {
			case p.Type().String() == "const int*":
				next = "int" // CUBLAS takes const int* for many things where it'd be an int in a normal blas call
			case p.Kind() == cc.Enum:
				next = GoTypeForEnum(p.Type(), n, blasEnums)
			default:
				next = GoTypeFor(p.Type(), n, voidPtrType)
			}
		}

		if next == this {
			buf.WriteString(n)
		} else {
			fmt.Fprintf(buf, "%s %s", n, this)
		}
	}

	buf.WriteString(") ")
	switch {
	case hasRet && d.Return.String() == "enum CUBLAS_STATUS { ... }":
		fmt.Fprintf(buf, "(retVal %s) {\n", retType)
	case hasRet && d.Return.Kind() != cc.Void:
		fmt.Fprintf(buf, " (%s, %s) {\n", retType, cToGoType[d.Return.String()])
	case !hasRet && d.Return.Kind() != cc.Void:
		fmt.Fprintf(buf, " %s {\n", cToGoType[d.Return.String()])
	default:
		buf.WriteString("{\n")
	}

}

func parameterChecks(buf *bytes.Buffer, d Declaration, rules []func(*bytes.Buffer, Declaration, Parameter) bool) {
	done := make(map[int]bool)
	for _, p := range d.Parameters() {
		for i, r := range rules {
			if done[i] {
				continue
			}
			done[i] = r(buf, d, p)
		}
	}
}

func cgoCall(buf *bytes.Buffer, d Declaration) {
	// if there is a "result" param, lift it out of the call
	var hasRet bool
	for _, p := range d.Parameters() {
		if p.Name() != "result" {
			continue
		}
		hasRet = true

		switch d.Name {
		case "cublasIsamax", "cublasIdamax", "cublasIcamax", "cublasIzamax", "cublasIsamin", "cublasIdamin", "cublasIcamin", "cublasIzamin":
			buf.WriteString("var ret C.int\n")
		default:
		}
	}

	if d.Return.String() == "enum CUBLAS_STATUS { ... }" {
		// fmt.Fprintf(buf, "return %s(", cToGoType[d.Return.String()])
		fmt.Fprintf(buf, "impl.e = status(")
	}

	fmt.Fprintf(buf, "C.%s(", cblasTocublas(d.Name))
	for i, p := range d.Parameters() {
		if p.Name() == "handle" {
			fmt.Fprintf(buf, "C.cublasHandle_t(impl.h)")
			continue
		}

		name := shorten(LowerCaseFirst(p.Name()))
		if p.Name() == "result" {
			name = "retVal"

		}

		if i != 0 {
			buf.WriteString(", ")
		}

		if p.Name() == "result" {
			switch d.Name {
			case "cublasIsamax", "cublasIdamax", "cublasIcamax", "cublasIzamax", "cublasIsamin", "cublasIdamin", "cublasIcamin", "cublasIzamin":
				buf.WriteString("&ret")
				continue
			default:
			}
		}

		if p.Type().Kind() == cc.Enum {
			buf.WriteString(CgoConversionForEnum(name, p.Type(), cgoEnums))
		} else {
			buf.WriteString(CgoConversionFor(name, p.Type(), cgoTypes))
		}
	}
	buf.WriteString(") ")
	switch {
	case hasRet && d.Return.String() == "enum CUBLAS_STATUS { ... }":
		switch d.Name {
		case "cublasIsamax", "cublasIdamax", "cublasIcamax", "cublasIzamax", "cublasIsamin", "cublasIdamin", "cublasIcamin", "cublasIzamin":
			buf.WriteString(")\n return int(ret)\n")
		default:
			buf.WriteString(")\n return retVal\n")
		}

	case !hasRet && d.Return.String() == "enum CUBLAS_STATUS { ... }":
		buf.WriteString(")\n")
	default:
		buf.WriteString("\n")
	}

}

var parameterCheckRules = []func(*bytes.Buffer, Declaration, Parameter) bool{
	trans,
	uplo,
	diag,
	side,

	shape,
	apShape,
	zeroInc,
	sidedShape,
	mvShape,
	rkShape,
	gemmShape,
	scalShape,
	amaxShape,
	nrmSumShape,
	vectorShape,
	othersShape,

	noWork,
}

func amaxShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasIsamax", "cublasIdamax", "cublasIcamax", "cublasIzamax", "cublasIsamin", "cublasIdamin", "cublasIcamin", "cublasIzamin":
	default:
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	fmt.Fprint(buf, `	if n == 0 || incX < 0 {
		return -1
	}
	if incX > 0 && (n-1)*incX >= len(x) {
		panic("blas: x index out of range")
	}
`)
	return true
}

func apShape(buf *bytes.Buffer, _ Declaration, p Parameter) bool {
	n := LowerCaseFirst(p.Name())
	if n != "ap" {
		return false
	}
	fmt.Fprint(buf, `	if n*(n+1)/2 > len(ap) {
		panic("blas: index of ap out of range")
	}
`)
	return true
}

func diag(buf *bytes.Buffer, _ Declaration, p Parameter) bool {
	if p.Name() != "Diag" {
		return false
	}
	fmt.Fprint(buf, `	if d != blas.NonUnit && d != blas.Unit {
		panic("blas: illegal diagonal")
	}
`)
	return true
}

func gemmShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSgemm", "cublasDgemm", "cublasCgemm", "cublasZgemm":
	default:
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	fmt.Fprint(buf, `	var rowA, colA, rowB, colB int
	if tA == blas.NoTrans {
		rowA, colA = m, k
	} else {
		rowA, colA = k, m
	}
	if tB == blas.NoTrans {
		rowB, colB = k, n
	} else {
		rowB, colB = n, k
	}
	if lda*(rowA-1)+colA > len(a) || lda < max(1, colA) {
		panic("blas: index of a out of range")
	}
	if ldb*(rowB-1)+colB > len(b) || ldb < max(1, colB) {
		panic("blas: index of b out of range")
	}
	if ldc*(m-1)+n > len(c) || ldc < max(1, n) {
		panic("blas: index of c out of range")
	}
`)
	return true
}

func mvShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSgbmv", "cublasDgbmv", "cublasCgbmv", "cublasZgbmv",
		"cublasSgemv", "cublasDgemv", "cublasCgemv", "cublasZgemv":
	default:
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	fmt.Fprint(buf, `	var lenX, lenY int
	if tA == blas.NoTrans {
		lenX, lenY = n, m
	} else {
		lenX, lenY = m, n
	}
	if (incX > 0 && (lenX-1)*incX >= len(x)) || (incX < 0 && (1-lenX)*incX >= len(x)) {
		panic("blas: x index out of range")
	}
	if (incY > 0 && (lenY-1)*incY >= len(y)) || (incY < 0 && (1-lenY)*incY >= len(y)) {
		panic("blas: y index out of range")
	}
`)
	return true
}

func noWork(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	var hasN, hasLda, hasLdb bool
	for _, p := range d.Parameters() {
		switch shorten(LowerCaseFirst(p.Name())) {
		case "n":
			hasN = true
		case "lda":
			hasLda = true
		case "ldb":
			hasLdb = true
		}
	}
	if !hasN || hasLda || hasLdb {
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	var value string
	switch d.Return.String() {
	case "int":
		value = " -1"
	case "float", "double":
		value = " 0"
	}
	fmt.Fprintf(buf, `	if n == 0 {
		return%s
	}
`, value)
	return true
}

func nrmSumShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSnrm2", "cublasDnrm2", "cublasScnrm2", "cublasDznrm2",
		"cublasSasum", "cublasDasum", "cublasScasum", "cublasDzasum":
	default:
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	fmt.Fprint(buf, `	if incX < 0 {
		return 0
	}
	if incX > 0 && (n-1)*incX >= len(x) {
		panic("blas: x index out of range")
	}
`)
	return true
}

func rkShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSsyrk", "cublasDsyrk", "cublasCsyrk", "cublasZsyrk",
		"cublasSsyr2k", "cublasDsyr2k", "cublasCsyr2k", "cublasZsyr2k",
		"cublasCherk", "cublasZherk", "cublasCher2k", "cublasZher2k":
	default:
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	fmt.Fprint(buf, `	var row, col int
	if t == blas.NoTrans {
		row, col = n, k
	} else {
		row, col = k, n
	}
`)
	has := make(map[string]bool)
	for _, p := range d.Parameters() {
		if p.Kind() != cc.Ptr {
			continue
		}
		has[shorten(LowerCaseFirst(p.Name()))] = true
	}
	for _, label := range []string{"a", "b"} {
		if has[label] {
			fmt.Fprintf(buf, `	if ld%[1]s*(row-1)+col > len(%[1]s) || ld%[1]s < max(1, col) {
		panic("blas: index of %[1]s out of range")
	}
`, label)
		}
	}
	if has["c"] {
		fmt.Fprint(buf, `	if ldc*(n-1)+n > len(c) || ldc < max(1, n) {
		panic("blas: index of c out of range")
	}
`)
	}

	return true
}

func scalShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSscal", "cublasDscal", "cublasCscal", "cublasZscal", "cublasCsscal":
	default:
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	fmt.Fprint(buf, `	if incX < 0 {
		return
	}
	if incX > 0 && (n-1)*incX >= len(x) {
		panic("blas: x index out of range")
	}
`)
	return true
}

func shape(buf *bytes.Buffer, _ Declaration, p Parameter) bool {
	switch n := LowerCaseFirst(p.Name()); n {
	case "m", "n", "k", "kL", "kU":
		fmt.Fprintf(buf, `	if %[1]s < 0 {
		panic("blas: %[1]s < 0")
	}
`, n)
		return false
	}
	return false
}

func side(buf *bytes.Buffer, _ Declaration, p Parameter) bool {
	if p.Name() != "Side" {
		return false
	}
	fmt.Fprint(buf, `	if s != blas.Left && s != blas.Right {
		panic("blas: illegal side")
	}
`)
	return true
}

func sidedShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	var hasS, hasA, hasB, hasC bool
	for _, p := range d.Parameters() {
		switch shorten(LowerCaseFirst(p.Name())) {
		case "s":
			hasS = true
		case "a":
			hasA = true
		case "b":
			hasB = true
		case "c":
			hasC = true
		}
	}
	if !hasS {
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	if hasA && hasB {
		fmt.Fprint(buf, `	var k int
	if s == blas.Left {
		k = m
	} else {
		k = n
	}
	if lda*(k-1)+k > len(a) || lda < max(1, k) {
		panic("blas: index of a out of range")
	}
	if ldb*(m-1)+n > len(b) || ldb < max(1, n) {
		panic("blas: index of b out of range")
	}
`)
	} else {
		return true
	}
	if hasC {
		fmt.Fprint(buf, `	if ldc*(m-1)+n > len(c) || ldc < max(1, n) {
		panic("blas: index of c out of range")
	}
`)
	}

	return true
}

func trans(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch n := shorten(LowerCaseFirst(p.Name())); n {
	case "t", "tA", "tB":
		switch {
		case strings.HasPrefix(d.Name, "cublasCh"), strings.HasPrefix(d.Name, "cublasZh"):
			fmt.Fprintf(buf, `	if %[1]s != blas.NoTrans && %[1]s != blas.ConjTrans {
		panic("blas: illegal transpose")
	}
`, n)
		case strings.HasPrefix(d.Name, "cublasCs"), strings.HasPrefix(d.Name, "cublasZs"):
			fmt.Fprintf(buf, `	if %[1]s != blas.NoTrans && %[1]s != blas.Trans {
		panic("blas: illegal transpose")
	}
`, n)
		default:
			fmt.Fprintf(buf, `	if %[1]s != blas.NoTrans && %[1]s != blas.Trans && %[1]s != blas.ConjTrans {
		panic("blas: illegal transpose")
	}
`, n)
		}
	}
	return false
}

func uplo(buf *bytes.Buffer, _ Declaration, p Parameter) bool {
	if p.Name() != "Uplo" {
		return false
	}
	fmt.Fprint(buf, `	if ul != blas.Upper && ul != blas.Lower {
		panic("blas: illegal triangle")
	}
`)
	return true
}

func vectorShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSgbmv", "cublasDgbmv", "cublasCgbmv", "cublasZgbmv",
		"cublasSgemv", "cublasDgemv", "cublasCgemv", "cublasZgemv",
		"cublasSscal", "cublasDscal", "cublasCscal", "cublasZscal", "cublasCsscal",
		"cublasIsamax", "cublasIdamax", "cublasIcamax", "cublasIzamax",
		"cublasSnrm2", "cublasDnrm2", "cublasScnrm2", "cublasDznrm2",
		"cublasSasum", "cublasDasum", "cublasScasum", "cublasDzasum":
		return true
	}

	var hasN, hasM, hasIncX, hasIncY bool
	for _, p := range d.Parameters() {
		switch shorten(LowerCaseFirst(p.Name())) {
		case "n":
			hasN = true
		case "m":
			hasM = true
		case "incX":
			hasIncX = true
		case "incY":
			hasIncY = true
		}
	}
	if !hasN && !hasM {
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	var label string
	if hasM {
		label = "m"
	} else {
		label = "n"
	}
	if hasIncX {
		fmt.Fprintf(buf, `	if (incX > 0 && (%[1]s-1)*incX >= len(x)) || (incX < 0 && (1-%[1]s)*incX >= len(x)) {
		panic("blas: x index out of range")
	}
`, label)
	}
	if hasIncY {
		fmt.Fprint(buf, `	if (incY > 0 && (n-1)*incY >= len(y)) || (incY < 0 && (1-n)*incY >= len(y)) {
		panic("blas: y index out of range")
	}
`)
	}
	return true
}

func zeroInc(buf *bytes.Buffer, _ Declaration, p Parameter) bool {
	switch n := LowerCaseFirst(p.Name()); n {
	case "incX":
		fmt.Fprintf(buf, `	if incX == 0 {
		panic("blas: zero x index increment")
	}
`)
	case "incY":
		fmt.Fprintf(buf, `	if incY == 0 {
		panic("blas: zero y index increment")
	}
`)
		return true
	}
	return false
}

func othersShape(buf *bytes.Buffer, d Declaration, p Parameter) bool {
	switch d.Name {
	case "cublasSgemm", "cublasDgemm", "cublasCgemm", "cublasZgemm",
		"cublasSsyrk", "cublasDsyrk", "cublasCsyrk", "cublasZsyrk",
		"cublasSsyr2k", "cublasDsyr2k", "cublasCsyr2k", "cublasZsyr2k",
		"cublasCherk", "cublasZherk", "cublasCher2k", "cublasZher2k":
		return true
	}

	has := make(map[string]bool)
	for _, p := range d.Parameters() {
		has[shorten(LowerCaseFirst(p.Name()))] = true
	}
	if !has["a"] || has["s"] {
		return true
	}

	if d.CParameters[len(d.CParameters)-1] != p.Parameter {
		return false // Come back later.
	}

	switch {
	case has["kL"] && has["kU"]:
		fmt.Fprintf(buf, `	if lda*(m-1)+kL+kU+1 > len(a) || lda < kL+kU+1 {
		panic("blas: index of a out of range")
	}
`)
	case has["m"]:
		fmt.Fprintf(buf, `	if lda*(m-1)+n > len(a) || lda < max(1, n) {
		panic("blas: index of a out of range")
	}
`)
	case has["k"]:
		fmt.Fprintf(buf, `	if lda*(n-1)+k+1 > len(a) || lda < k+1 {
		panic("blas: index of a out of range")
	}
`)
	default:
		fmt.Fprintf(buf, `	if lda*(n-1)+n > len(a) || lda < max(1, n) {
		panic("blas: index of a out of range")
	}
`)
	}

	return true
}