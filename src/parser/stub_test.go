package parser

import (
	"testing"
)

func TestStubFuncDeclShape(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "stub func with param and return type",
			src:  "stub func args() []string",
			want: "" +
				"StubFuncDecl \"stub\"\n" +
				"  Ident \"args\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"  ArrayType\n" +
				"    <missing>\n" +
				"    Ident \"string\"\n",
		},
		{
			name: "stub func with type params and multi-return",
			src:  "stub func AnyAs[T](a Any) (T, bool)",
			want: "" +
				"StubFuncDecl \"stub\"\n" +
				"  Ident \"AnyAs\"\n" +
				"  TypeParamList\n" +
				"    Ident \"T\"\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"a\"\n" +
				"      Ident \"Any\"\n" +
				"  MultiReturnType\n" +
				"    Ident \"T\"\n" +
				"    Ident \"bool\"\n",
		},
		{
			name: "stub func with no return type",
			src:  "stub func print(x Any)",
			want: "" +
				"StubFuncDecl \"stub\"\n" +
				"  Ident \"print\"\n" +
				"  <missing>\n" +
				"  ParamList\n" +
				"    Param\n" +
				"      Ident \"x\"\n" +
				"      Ident \"Any\"\n" +
				"  <missing>\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tree, n := parseDeclSrc(t, tt.src)
			got := tree.Dump(n)
			if got != tt.want {
				t.Errorf("Dump(%q):\n got:\n%s\nwant:\n%s", tt.src, got, tt.want)
			}
		})
	}
}
