package ast

import (
	"testing"

	"llvm_lang/src/enums"
	"llvm_lang/src/lexer"
)

func TestIsTestFuncName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"TestFoo", true},
		{"TestX", true},
		{"Test", false},            // no suffix at all
		{"Testing", false},         // "ing" isn't uppercase
		{"testFoo", false},         // wrong case entirely
		{"BenchmarkFoo", false},    // different convention
		{"Test_underscore", false}, // "_" isn't uppercase - stricter than Go's own "not lowercase" rule, by design here
	}
	for _, c := range cases {
		if got := IsTestFuncName(c.name); got != c.want {
			t.Errorf("IsTestFuncName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// buildPointerToQualifiedType builds *pkg.name (PointerType->MemberExpr->
// Ident), the exact shape parseTypeExpr produces for a package-qualified
// pointer type (src/parser/stmt.go's own doc comment).
func buildPointerToQualifiedType(t *testing.T, pkg, name string) (*Tree, NodeIndex) {
	t.Helper()
	file := lexer.NewFile("t.ll", "*"+pkg+"."+name)
	tree := NewTree(file)

	objTok := lexer.Token{Start: 1, End: lexer.Pos(1 + len(pkg))}
	obj := tree.NewNode(enums.NodeKinds.Ident, objTok, Span{Start: objTok.Start, End: objTok.End})

	fieldTok := lexer.Token{Start: lexer.Pos(2 + len(pkg)), End: lexer.Pos(2 + len(pkg) + len(name))}
	member := tree.NewNode(enums.NodeKinds.MemberExpr, fieldTok, Span{Start: objTok.Start, End: fieldTok.End}, obj)

	ptr := tree.NewNode(enums.NodeKinds.PointerType, lexer.Token{}, Span{Start: 0, End: fieldTok.End}, member)
	return tree, ptr
}

func TestIsPointerToQualifiedType(t *testing.T) {
	tree, ptr := buildPointerToQualifiedType(t, "test", "Runner")

	if !tree.IsPointerToQualifiedType(ptr, "test", "Runner") {
		t.Error("want *test.Runner to match (test, Runner)")
	}
	if tree.IsPointerToQualifiedType(ptr, "test", "Wrong") {
		t.Error("want *test.Runner to NOT match (test, Wrong)")
	}
	if tree.IsPointerToQualifiedType(ptr, "wrong", "Runner") {
		t.Error("want *test.Runner to NOT match (wrong, Runner)")
	}
	if tree.IsPointerToQualifiedType(InvalidNode, "test", "Runner") {
		t.Error("want InvalidNode to never match")
	}

	// A bare (non-pointer) qualified type must not match either - only
	// the *T form is a valid test-entry-point parameter.
	_, memberOnly := buildPointerToQualifiedType(t, "test", "Runner")
	memberTree, _ := buildPointerToQualifiedType(t, "test", "Runner")
	bareMember := memberTree.Child(memberOnly, 0)
	if memberTree.IsPointerToQualifiedType(bareMember, "test", "Runner") {
		t.Error("want a bare (non-pointer) MemberExpr to NOT match")
	}
}
