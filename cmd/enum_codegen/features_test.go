package main

import (
	"strings"
	"testing"
)

const aliasDenseSpec = `
package: sema
type: TypeKind
underlying: int
denseTable: true
aliasField: aliasOf
tsOut: type_kind.ts
ktOut: TypeKind.kt
ktPackage: dev.llvm.lang.sema
values:
  - name: Invalid
  - name: I32
    display: "int"
    bits: 32
    integer: true
  - name: Int
    aliasOf: I32
  - name: U8
    display: "u8"
    bits: 8
    integer: true
    unsigned: true
  - name: Struct
  - name: MAX
    wire: 4
    sentinel: true
`

func TestAliasAndDenseTableGo(t *testing.T) {
	p := mustParse(t, aliasDenseSpec)
	out, err := renderGo(p.spec, p.fields, p.entries, "type_kind.yml")
	if err != nil {
		t.Fatalf("renderGo: %v", err)
	}
	got := string(out)
	compact := strings.Join(strings.Fields(got), " ")

	for _, want := range []string{
		"TypeKindInt = TypeKindI32",
		"TypeKindU8 TypeKind = 2",
		"TypeKindStruct TypeKind = 3",
		"TypeKindMAX TypeKind = 4",
		"var typeKindInfos = [...]TypeKindInfo{",
		"if uint64(v) >= uint64(len(typeKindInfos))",
		"return uint64(v) < uint64(len(typeKindInfos))",
	} {
		if !strings.Contains(compact, want) && !strings.Contains(got, want) {
			t.Errorf("Go output missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "map[TypeKind]TypeKindInfo") {
		t.Error("dense metadata table must not use a map")
	}
	for _, forbidden := range []string{
		"Int TypeKind\n",
		"Int: TypeKindInt",
		`"int": TypeKindInt`,
		"TypeKindInt,",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf("alias leaked into generated runtime data as %q", forbidden)
		}
	}
}

func TestAliasRenderersExcludeAliasFromLookups(t *testing.T) {
	p := mustParse(t, aliasDenseSpec)

	tsOut, err := renderTS(p.spec, p.fields, p.entries, "type_kind.yml", "")
	if err != nil {
		t.Fatalf("renderTS: %v", err)
	}
	ts := string(tsOut)
	if !strings.Contains(ts, "export const TypeKindInt: TypeKind = TypeKinds.I32;") {
		t.Errorf("TS alias missing\n---\n%s", ts)
	}
	if strings.Contains(ts, `"Int": TypeKinds.Int`) {
		t.Error("TS alias must not appear in ByName")
	}

	ktOut, err := renderKotlin(p.spec, p.fields, p.entries, "type_kind.yml")
	if err != nil {
		t.Fatalf("renderKotlin: %v", err)
	}
	kt := string(ktOut)
	for _, want := range []string{
		"package dev.llvm.lang.sema",
		"@JvmInline",
		"public value class TypeKind(public val wire: Int)",
		"public val I32: TypeKind = TypeKind(1)",
		"public val TypeKindInt: TypeKind = TypeKind.I32",
		"public fun fromWire(wire: Int): TypeKind? = byWire[wire]",
		"public fun parse(name: String): TypeKind?",
		"public val display: String?,",
		"public val bits: Int?,",
	} {
		if !strings.Contains(kt, want) {
			t.Errorf("Kotlin output missing %q\n---\n%s", want, kt)
		}
	}
	if strings.Contains(kt, `"Int" to Int`) {
		t.Error("Kotlin alias must not appear in byName")
	}
}

func TestAliasValidation(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing target",
			body: "  - name: Alias\n    alias: Missing\n",
			want: "must be an earlier member",
		},
		{
			name: "forward target",
			body: "  - name: Alias\n    alias: Real\n  - name: Real\n",
			want: "must be an earlier member",
		},
		{
			name: "alias chain",
			body: "  - name: Real\n  - name: First\n    alias: Real\n  - name: Second\n    alias: First\n",
			want: "itself an alias",
		},
		{
			name: "wire",
			body: "  - name: Real\n  - name: Alias\n    alias: Real\n    wire: 4\n",
			want: "combined with wire",
		},
		{
			name: "sentinel",
			body: "  - name: Real\n  - name: Alias\n    alias: Real\n    sentinel: false\n",
			want: "combined with sentinel",
		},
		{
			name: "metadata",
			body: "  - name: Real\n  - name: Alias\n    alias: Real\n    display: alias\n",
			want: "cannot have metadata",
		},
		{
			name: "sentinel target",
			body: "  - name: MAX\n    sentinel: true\n  - name: Alias\n    alias: MAX\n",
			want: "is a sentinel",
		},
		{
			name: "empty target",
			body: "  - name: Alias\n    alias:\n",
			want: "alias target is required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "package: p\ntype: T\naliasField: alias\nvalues:\n" + tc.body
			_, err := parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse error = %v, want text %q", err, tc.want)
			}
		})
	}
}

func TestAliasMetadataRemainsCompatibleWithoutAliasField(t *testing.T) {
	p := mustParse(t, `
package: p
type: Stat
underlying: string
iterators: [alias]
values:
  - name: Hp
    alias: hp
  - name: Mp
`)
	if len(aliasEntries(p.entries)) != 0 {
		t.Fatal("alias metadata was interpreted as a source-level alias")
	}
	if len(p.fields) != 1 || p.fields[0].Key != "alias" {
		t.Fatalf("metadata fields = %#v, want alias", p.fields)
	}
	out, err := renderGo(p.spec, p.fields, p.entries, "stat.yml")
	if err != nil {
		t.Fatalf("renderGo: %v", err)
	}
	if !strings.Contains(string(out), "func (v Stat) Alias() string") {
		t.Errorf("legacy alias metadata accessor missing\n---\n%s", out)
	}
}

func TestAliasFieldRejectsBuiltInMemberKeys(t *testing.T) {
	for _, key := range []string{"name", "wire", "sentinel"} {
		src := "package: p\ntype: T\naliasField: " + key + "\nvalues:\n  - name: A\n"
		_, err := parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "built-in member key") {
			t.Errorf("aliasField %q error = %v", key, err)
		}
	}

	_, err := parse([]byte(`
package: p
type: T
aliasField: aliasOf
fields:
  aliasOf: string
values:
  - name: A
`))
	if err == nil || !strings.Contains(err.Error(), "declared as metadata") {
		t.Errorf("aliasField metadata conflict error = %v", err)
	}
}

func TestDenseTableValidation(t *testing.T) {
	tests := []struct {
		name string
		spec string
		want string
	}{
		{
			name: "non integer",
			spec: "underlying: string\nvalues:\n  - name: A\n",
			want: "integer underlying",
		},
		{
			name: "negative",
			spec: "values:\n  - name: A\n    wire: -1\n",
			want: "negative wire",
		},
		{
			name: "gap",
			spec: "values:\n  - name: A\n    wire: 0\n  - name: B\n    wire: 2\n",
			want: "contiguous",
		},
		{
			name: "duplicate",
			spec: "values:\n  - name: A\n    wire: 0\n  - name: B\n    wire: 0\n",
			want: "duplicate wire",
		},
		{
			name: "empty",
			spec: "values:\n  - name: MAX\n    wire: 0\n    sentinel: true\n",
			want: "at least one live member",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "package: p\ntype: T\ndenseTable: true\n" + tc.spec
			_, err := parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parse error = %v, want text %q", err, tc.want)
			}
		})
	}
}

func TestRenderKotlinReferencesAndTypes(t *testing.T) {
	const source = `
package: source
type: Source
underlying: uint8
ktPackage: dev.example.source
values:
  - name: One
`
	const target = `
package: target
type: Target
underlying: string
ktPackage: dev.example.target
fields:
  source: Source
  sources: "[]Source"
  weight: float32
values:
  - name: Main
    source: One
    sources: [One]
    weight: 1.5
  - name: Other
`
	all := build(t, source, target)
	out, err := renderKotlin(all[1].spec, all[1].fields, all[1].entries, "target.yml")
	if err != nil {
		t.Fatalf("renderKotlin: %v", err)
	}
	got := string(out)
	for _, want := range []string{
		"import dev.example.source.Source",
		"public value class Target(public val wire: String)",
		"public val source: Source?,",
		"public val sources: List<Source>?,",
		"public val weight: Float?,",
		"source = Source.One,",
		"sources = listOf(Source.One),",
		"weight = 1.5f,",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Kotlin output missing %q\n---\n%s", want, got)
		}
	}
}

func TestKotlinPrimitiveMappingsAndLiterals(t *testing.T) {
	types := map[string]string{
		"bool": "Boolean", "string": "String",
		"int8": "Byte", "int16": "Short", "int32": "Int", "int64": "Long",
		"uint8": "UByte", "uint16": "UShort", "uint32": "UInt", "uint64": "ULong",
		"float32": "Float", "float64": "Double",
	}
	for goType, want := range types {
		if got := kotlinBasicType(goType); got != want {
			t.Errorf("kotlinBasicType(%q) = %q, want %q", goType, got, want)
		}
	}

	tests := []struct {
		goType string
		value  string
		want   string
	}{
		{"uint8", "7", "7u"},
		{"uint64", "7", "7uL"},
		{"int64", "7", "7L"},
		{"float32", "7", "7.0f"},
		{"float64", "7", "7.0"},
		{"float64", "1e3", "1e3"},
		{"string", "price: $5", `"price: \$5"`},
	}
	for _, tc := range tests {
		node := scalarNode(tc.value, tc.goType == "string")
		if got := kotlinBasicLit(tc.goType, node); got != tc.want {
			t.Errorf("kotlinBasicLit(%q, %q) = %q, want %q", tc.goType, tc.value, got, tc.want)
		}
	}
}

func TestKotlinPackageValidationAndEscaping(t *testing.T) {
	p := mustParse(t, `
package: p
type: T
ktPackage: dev.when.example
values:
  - name: A
`)
	out, err := renderKotlin(p.spec, p.fields, p.entries, "t.yml")
	if err != nil {
		t.Fatalf("renderKotlin: %v", err)
	}
	if !strings.Contains(string(out), "package dev.`when`.example") {
		t.Errorf("Kotlin keyword package segment was not escaped\n---\n%s", out)
	}

	p.spec.KTPackage = "dev.bad-name"
	if _, err := renderKotlin(p.spec, p.fields, p.entries, "t.yml"); err == nil {
		t.Fatal("expected an invalid Kotlin package error")
	}
}
