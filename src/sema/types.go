package sema

import (
	"fmt"
	"strings"
)

// TypeKind classifies a Type. Two kinds exist purely for error recovery and
// void-call bookkeeping, not because the language has values of them:
//   - TypeInvalid marks a type that couldn't be determined because of an
//     earlier error - so type-checking has something to hand back instead of
//     forcing a nil check at every call site, and so a single root-cause
//     error doesn't cascade into a wall of follow-on ones (any check
//     involving a TypeInvalid operand is skipped, not re-reported).
//   - TypeVoid is a function's "return type" when it declares none at all
//     (`func f() { }`) - distinct from TypeInvalid so a bare `return` in a
//     function with no declared return type isn't treated as an error, and
//     so using such a call's result as a value gets a real diagnostic
//     ("does not return a value") rather than silently swallowing it as
//     just another already-reported error.
//
// TypeI8/TypeI16/TypeI32/TypeI64 are real, distinct signed integer widths;
// TypeF32/TypeF64 are real, distinct floating-point widths (see AGENTS.md's
// Types section). There is no separate TypeInt constant - see TypeInt's own
// doc comment below for why "int" is a synonym for TypeI32, not a second
// TypeKind value that merely happens to compare equal to it.
//
// TypeFunc is a first-class function value's type - a free function
// referenced without being called (`add`, not `add(...)`), or a variable/
// parameter declared with a function-type annotation (`func(int) int`).
// See Type's own Params/Return fields and LANGUAGE.md's "First-class
// functions" section for the representation and what's (and isn't) covered
// this round - bound method values are explicitly out of scope for now.
//
// TypeUntypedInt/TypeUntypedFloat are Go's own "untyped constant" model,
// narrowed to numeric literals only - bool/string literals always have
// exactly one representation, so they never need this. A NumberLit starts
// life as one of these two (see typecheck.go's checkNumberLit) and stays
// untyped until some context resolves it to a concrete numeric type: a
// declared variable's type, another concretely-typed operand in a binary
// expression, a parameter's declared type, a function's declared return
// type, a composite-literal element's expected type, or - absent any such
// context - Go's own untyped-constant defaulting rule (untyped int -> i32,
// untyped float -> f64). See resolveNumericOperands/checkAssignable/
// retypeUntyped in typecheck.go and AGENTS.md's Types section.
type TypeKind int

const (
	TypeInvalid TypeKind = iota
	TypeVoid

	TypeI8
	TypeI16
	TypeI32
	TypeI64

	TypeF32
	TypeF64

	TypeString
	TypeBool
	TypeStruct
	TypeArray
	TypeFunc

	TypeUntypedInt
	TypeUntypedFloat
)

// TypeInt is a synonym for TypeI32, not a distinct TypeKind value: "int" in
// this language has always meant exactly a 32-bit signed integer (see
// AGENTS.md's Types section and BLOCKERS.md's "int is 32-bit" entry). Now
// that i8/i16/i32/i64 exist as their own real, named types, keeping "int" as
// a second constant that merely happens to Equal TypeI32 would be exactly
// the kind of parallel, hand-synced representation of the same type this
// project avoids elsewhere - every switch/comparison anywhere in this
// package or codegen only ever needs to look at TypeI32; "int" and "i32" are
// simply two source-level spellings of the identical Type.
const TypeInt = TypeI32

// Type is the result of type-checking one expression or type position.
// Structs are identified by their *StructInfo (built once by Resolve and
// shared, so two Types both naming struct Point always point at the exact
// same StructInfo - see Equal) rather than by name, which would need a
// string compare and can't distinguish shadowed/duplicate declarations were
// this language ever to grow multiple files or packages.
//
// Array and Func are both recursive cases (an array of arrays, or a
// function returning a function, are both legal), hence Elem/Return are
// *Type rather than Type - a value Type containing itself by value can't
// compile.
type Type struct {
	Kind TypeKind

	// Struct is set when Kind == TypeStruct: the struct's catalog, as
	// already built by Resolve.
	Struct *StructInfo

	// Elem, Size, and Dynamic are set when Kind == TypeArray.
	// Dynamic distinguishes `[]T` (parsed, but semantically rejected for
	// now - see AGENTS.md's Arrays section) from `[N]T`; Size is only
	// meaningful when Dynamic is false.
	Elem    *Type
	Size    int64
	Dynamic bool

	// Params and Return are set when Kind == TypeFunc: a function value's
	// parameter types and return type (TypeVoid for a function type that
	// declares none, e.g. `func(int)` - see LANGUAGE.md's "First-class
	// functions" section). Params is a plain []Type - a slice header is
	// already an indirection, so no self-containment problem there, unlike
	// Return - a function type may itself return another function type, so
	// Return needs the same *Type indirection Elem uses above.
	Params []Type
	Return *Type
}

var (
	invalidType = Type{Kind: TypeInvalid}
	voidType    = Type{Kind: TypeVoid}

	i8Type  = Type{Kind: TypeI8}
	i16Type = Type{Kind: TypeI16}
	i32Type = Type{Kind: TypeI32}
	i64Type = Type{Kind: TypeI64}

	f32Type = Type{Kind: TypeF32}
	f64Type = Type{Kind: TypeF64}

	stringType = Type{Kind: TypeString}
	boolType   = Type{Kind: TypeBool}

	untypedIntType   = Type{Kind: TypeUntypedInt}
	untypedFloatType = Type{Kind: TypeUntypedFloat}
)

// IsInvalid reports whether t is the TypeInvalid error-recovery sentinel.
func (t Type) IsInvalid() bool { return t.Kind == TypeInvalid }

// IsIntegerKind reports whether t is a signed integer type of any width
// (i8/i16/i32/i64 - "int" is exactly i32, see TypeInt's doc comment) or the
// untyped-int constant kind.
func (t Type) IsIntegerKind() bool {
	switch t.Kind {
	case TypeI8,
		TypeI16,
		TypeI32,
		TypeI64,
		TypeUntypedInt:
		return true
	default:
		return false
	}
}

// IsFloatKind reports whether t is a floating-point type of any width
// (f32/f64) or the untyped-float constant kind.
func (t Type) IsFloatKind() bool {
	switch t.Kind {
	case TypeF32,
		TypeF64,
		TypeUntypedFloat:
		return true
	default:
		return false
	}
}

// IsNumeric reports whether t is any numeric type at all - a concrete
// integer or float width, or either untyped-constant kind.
func (t Type) IsNumeric() bool {
	return t.IsIntegerKind() || t.IsFloatKind()
}

// IsUntyped reports whether t is one of the two untyped-constant kinds a
// numeric literal starts life as, before context resolves it to a concrete
// type - see checkNumberLit/resolveNumericOperands/checkAssignable in
// typecheck.go, and AGENTS.md's Types section.
func (t Type) IsUntyped() bool {
	return t.Kind == TypeUntypedInt || t.Kind == TypeUntypedFloat
}

// Bits reports the concrete bit width of a numeric Type (i8/i16/i32/i64,
// f32/f64) - meaningful only for those six concrete kinds. codegen's
// genConversion is the only caller, deciding sext/trunc vs fpext/fptrunc by
// comparing two Bits() results; never called on an untyped/non-numeric Type.
func (t Type) Bits() int {
	switch t.Kind {
	case TypeI8:
		return 8
	case TypeI16:
		return 16
	case TypeI32:
		return 32
	case TypeI64:
		return 64
	case TypeF32:
		return 32
	case TypeF64:
		return 64
	default:
		panic(fmt.Sprintf("sema: Bits called on non-numeric type %s", t))
	}
}

// Equal reports whether t and u are the exact same type. There is no
// implicit conversion anywhere in this language yet (see AGENTS.md's
// Operators section) - every assignment/argument/return check is this
// comparison, nothing looser. An untyped constant is expected to already be
// resolved to a concrete type (see checkAssignable/resolveNumericOperands)
// before it ever reaches Equal - two untyped Types compare Equal only to
// another of the exact same untyped kind, never to a concrete one.
func (t Type) Equal(u Type) bool {
	if t.Kind != u.Kind {
		return false
	}
	switch t.Kind {
	case TypeStruct:
		return t.Struct == u.Struct
	case TypeArray:
		if t.Dynamic != u.Dynamic {
			return false
		}
		if !t.Dynamic && t.Size != u.Size {
			return false
		}
		return t.Elem.Equal(*u.Elem)
	case TypeFunc:
		if len(t.Params) != len(u.Params) {
			return false
		}
		for i := range t.Params {
			if !t.Params[i].Equal(u.Params[i]) {
				return false
			}
		}
		return t.Return.Equal(*u.Return)
	default:
		return true
	}
}

// String renders t for diagnostics, e.g. "int", "i64", "f64", "Point",
// "[5]int", "[]int". TypeI32 prints as "int" rather than "i32" - "int" is
// this language's long-established, most commonly written spelling for
// exactly this type (see TypeInt's doc comment); "i32" is simply an
// alternative source spelling for the same Type, which String() can't
// distinguish from "int" anyway since both produce the identical Type value.
func (t Type) String() string {
	switch t.Kind {
	case TypeInvalid:
		return "<invalid>"
	case TypeVoid:
		return "void"
	case TypeI8:
		return "i8"
	case TypeI16:
		return "i16"
	case TypeI32:
		return "int"
	case TypeI64:
		return "i64"
	case TypeF32:
		return "f32"
	case TypeF64:
		return "f64"
	case TypeString:
		return "string"
	case TypeBool:
		return "bool"
	case TypeStruct:
		if t.Struct == nil {
			return "<struct>"
		}
		return t.Struct.Symbol.Name
	case TypeArray:
		if t.Dynamic {
			return "[]" + t.Elem.String()
		}
		return fmt.Sprintf("[%d]%s", t.Size, t.Elem.String())
	case TypeFunc:
		parts := make([]string, len(t.Params))
		for i, p := range t.Params {
			parts[i] = p.String()
		}
		s := "func(" + strings.Join(parts, ", ") + ")"
		if t.Return != nil && t.Return.Kind != TypeVoid {
			s += " " + t.Return.String()
		}
		return s
	case TypeUntypedInt:
		return "untyped int"
	case TypeUntypedFloat:
		return "untyped float"
	default:
		return "<unknown type>"
	}
}
