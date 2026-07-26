// Package codewriter builds source text with automatic indentation and
// delimited blocks so generator code can mirror the shape of what it emits.
//
// It is language-agnostic: braces, parens, comment prefixes, and indent
// units are configurable. Typical use:
//
//	w := codewriter.New()
//	w.Comment("Code generated. DO NOT EDIT.")
//	w.Linef("package %s", pkg)
//	w.Blank()
//	w.Paren("import", func() {
//		w.Line(`"fmt"`)
//	})
//	w.Blank()
//	w.Bracef("type %s struct", name, func() {
//		w.Linef("%s %s", field, typ)
//	})
//	src := w.Bytes()
package codewriter
