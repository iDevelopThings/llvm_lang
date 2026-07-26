package codewriter_test

import (
	"fmt"

	"llvm_lang/src/codewriter"
)

func ExampleWriter() {
	w := codewriter.New()
	w.Comment("Code generated. DO NOT EDIT.")
	w.Line("package demo")
	w.Blank()
	w.Paren("import", func() {
		w.Line(`"fmt"`)
	})
	w.Blank()
	w.Bracef("func %s(%s %s)", "Greet", "name", "string", func() {
		w.Line(`fmt.Println("hi", name)`)
	})
	fmt.Print(w.String())
	// Output:
	// // Code generated. DO NOT EDIT.
	// package demo
	//
	// import (
	// 	"fmt"
	// )
	//
	// func Greet(name string) {
	// 	fmt.Println("hi", name)
	// }
}
