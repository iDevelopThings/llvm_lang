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
// TypeU8/TypeU16/TypeU32/TypeU64 are their unsigned counterparts, identical
// in every way except signedness (see IsUnsigned and LANGUAGE.md's Types
// section); TypeF32/TypeF64 are real, distinct floating-point widths. There
// is no separate TypeInt constant - see TypeInt's own doc comment below for
// why "int" is a synonym for TypeI32, not a second TypeKind value that merely
// happens to compare equal to it. There is deliberately no unsigned analogue:
// "int" is special only as this language's oldest int type, and no single
// unsigned width holds that role.
//
// TypeFunc is a first-class function value's type - a free function
// referenced without being called (`add`, not `add(...)`), or a variable/
// parameter declared with a function-type annotation (`func(int) int`).
// See Type's own Params/Return fields and LANGUAGE.md's "First-class
// functions" section for the representation and what's (and isn't) covered
// this round - bound method values are explicitly out of scope for now.
//
// TypePointer is a real pointer type (`*T` - see LANGUAGE.md's "Pointers"
// section): `&x` (address-of an addressable expression), `new T(...)`/
// `new T{...}` (a heap allocation), and a `*T`-typed var/param/field all
// produce one. Reuses Type's own Elem field (the pointee type) exactly like
// TypeArray already does for its element type - two pointer types are equal
// iff their Elem types are (see Equal), the same structural-equality
// pattern TypeArray/TypeFunc already use.
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
//
// TypeMultiReturn is the type of a multi-return function call's result - "this
// expression yields N values" (see LANGUAGE.md's "Go-style multi-return
// values" section) - never a real, storable value type: it exists purely so
// checkValueExpr has something to reject everywhere except the two specific
// positions that actually consume it (a return statement matching the
// enclosing function's own multi-return type, or the sole right-hand side of
// a matching multi-target `:=`/`=` - see checkMultiValueReturn/
// checkDestructureSource, typecheck.go). Reuses Type's own Params field for
// the N component types (see Type's own doc comment) - the same "a Type
// holding several component Types" shape TypeFunc already established for its
// own Params, just without a Return of its own (a multi-return type is never
// itself callable). A function's own declared `(T1, T2, ...)` return-type
// list (MultiReturnType, ast.Node) becomes exactly this Type via
// multiReturnTypeFromNode - funcSignature.Return holds it directly, with no
// separate "is this multi" flag needed anywhere else in this pass.
//
// TypeGenerator is a generator function's own call result (see LANGUAGE.md's
// "Generator functions" section: `func Range(a, b int) yield int { ... }`) -
// never a real, storable value type, mirroring TypeMultiReturn's own
// "exists purely to be rejected everywhere except its one legal consuming
// position" role: a generator call's result is legal ONLY as a RangeForStmt's
// own subject expression (checkRangeForStmt/checkValueExprAllowGenerator),
// everywhere else it's a clean diagnostic. Reuses Type's own Elem field (the
// yielded element type) - the same "wraps one other Type" field
// TypeArray/TypePointer already share.
//
// TypeCoroutine is the type of calling an `async func` (see LANGUAGE.md's
// "Coroutines" section: `h := DoThing(args)`) - unlike TypeGenerator/
// TypeMultiReturn, a real, storable, non-copyable value (see
// typeIsNonCopyable/IsNonCopyable below): the whole point is a caller can
// hold the handle in a variable and drive it by hand (resume/done/delete).
// Reuses Type's own Elem field for the coroutine's own declared final-result
// type - the same "wraps one other Type" convention TypeArray/TypePointer/
// TypeGenerator already share. Async functions declare no return type this
// round (see LANGUAGE.md's "Coroutines" section for why), so Elem is always
// TypeVoid for now - kept as a real field rather than omitted so a future
// round can read a finished coroutine's own result with no representation
// change here.
//
// TypeUntypedNil is the predeclared `nil` identifier's own starting type
// (see LANGUAGE.md's "Pointers" section), modeled directly on that same
// untyped-constant precedent but deliberately scoped to pointer types only -
// this language has no general "zero value"/nil concept yet, so `nil` isn't
// folded into IsUntyped/IsNumeric (which every other numeric-untyped code
// path here assumes means "numeric") - it's handled by its own small set of
// call sites instead (checkAssignable, checkEqualityOperands, defaultIfUntyped).
//
// TypeCString is a raw C string (`char*`) for FFI (see LANGUAGE.md's
// "External functions (FFI)" section) - unlike TypeString's own {ptr, i32}
// fat struct, it's a single pointer with no length, so it can legally cross
// an extern func signature (isFFISafeType). Only reachable via an
// explicit conversion (`cstring(s)`/`string(cs)`, checkConversionCall) -
// there is no cstring literal syntax. Deliberately excluded from
// typeIsComparable/typeIsPrintable: neither `==` nor `print` has a defined
// lowering for it.
//
// TypeCFunc is a bare C function pointer type (`cfunc(T1, T2) R` - see
// LANGUAGE.md's "External functions (FFI)" section) - deliberately its own
// TypeKind, never folded into TypeFunc with a flag: unlike TypeFunc's fat
// `{fnPtr, ctxPtr}` closure value (codegen's funcValTy), a cfunc value is a
// single, bare function pointer with no capture context at all, callable
// with real C-ABI marshaling and no leading ctxPtr. Shares TypeFunc's own
// Params/Return representation exactly (see Type's own doc comment) - only
// the calling convention/lowering differs, not the shape. Only a direct
// reference to a top-level FuncDecl/ExternFuncDecl may ever become one
// (checkAssignable's own func-to-cfunc conversion, typecheck.go) - a
// function literal or any other function value with real captures is a
// compile error, since there is no trampoline to synthesize one this round.
type TypeKind int

const (
	TypeInvalid TypeKind = iota
	TypeVoid

	TypeI8
	TypeI16
	TypeI32
	TypeI64

	TypeU8
	TypeU16
	TypeU32
	TypeU64

	TypeF32
	TypeF64

	TypeString
	TypeCString
	TypeBool
	TypeStruct
	TypeArray
	TypeFunc
	TypeCFunc
	TypePointer
	TypeMap
	TypeEnum

	TypeUntypedInt
	TypeUntypedFloat
	TypeUntypedNil

	TypeMultiReturn
	TypeGenerator
	TypeCoroutine
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

	// Enum is set when Kind == TypeEnum: the enum's catalog, as already built
	// by Resolve - EnumInfo mirrors StructInfo's own "identified by pointer,
	// not name" reasoning exactly (see Equal below) - two Types both naming
	// enum Shape always point at the exact same EnumInfo. See LANGUAGE.md's
	// "Enums" section.
	Enum *EnumInfo

	// Elem, Size, and Dynamic are set when Kind == TypeArray.
	// Dynamic distinguishes `[]T` (parsed, but semantically rejected for
	// now - see AGENTS.md's Arrays section) from `[N]T`; Size is only
	// meaningful when Dynamic is false.
	//
	// Elem alone (Size/Dynamic unused) is also set when Kind == TypePointer -
	// a pointer's own pointee type (see LANGUAGE.md's "Pointers" section) -
	// reusing the same field TypeArray already has for exactly the same
	// reason: both are simple "wraps one other Type" cases.
	Elem    *Type
	Size    int64
	Dynamic bool

	// Key is set when Kind == TypeMap: the map's own declared key type
	// (`map[K]V`'s K - see LANGUAGE.md's "Maps" section). Elem doubles as the
	// map's *value* type (V) for this Kind too - the same "wraps one other
	// Type" field TypeArray/TypePointer already share, just with a second
	// wrapped Type of its own (Key) a map alone needs, since unlike an array
	// or a pointer a map has two independent type parameters, not one.
	Key *Type

	// Params and Return are set when Kind == TypeFunc or TypeCFunc: a
	// function value's parameter types and return type (TypeVoid for a
	// function type that declares none, e.g. `func(int)` - see LANGUAGE.md's
	// "First-class functions" and "External functions (FFI)" sections).
	// Params is a plain []Type - a slice header is already an indirection,
	// so no self-containment problem there, unlike Return - a function type
	// may itself return another function type, so Return needs the same
	// *Type indirection Elem uses above.
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

	u8Type  = Type{Kind: TypeU8}
	u16Type = Type{Kind: TypeU16}
	u32Type = Type{Kind: TypeU32}
	u64Type = Type{Kind: TypeU64}

	f32Type = Type{Kind: TypeF32}
	f64Type = Type{Kind: TypeF64}

	stringType  = Type{Kind: TypeString}
	cstringType = Type{Kind: TypeCString}
	boolType    = Type{Kind: TypeBool}

	untypedIntType   = Type{Kind: TypeUntypedInt}
	untypedFloatType = Type{Kind: TypeUntypedFloat}
	untypedNilType   = Type{Kind: TypeUntypedNil}
)

// IsInvalid reports whether t is the TypeInvalid error-recovery sentinel.
func (t Type) IsInvalid() bool { return t.Kind == TypeInvalid }

// Underlying derefs one TypePointer level, returning t unchanged for any
// other Kind - a pointer-to-struct/enum's own member access auto-derefs
// (see LANGUAGE.md's "Pointers" section: `p.field` behaves like
// `(*p).field`), and every member-lookup site needs the identical step.
func (t Type) Underlying() Type {
	if t.Kind == TypePointer {
		return *t.Elem
	}
	return t
}

// IsIntegerKind reports whether t is an integer type of any width, signed
// (i8/i16/i32/i64 - "int" is exactly i32, see TypeInt's doc comment) or
// unsigned (u8/u16/u32/u64), or the untyped-int constant kind. Two integer
// kinds sharing IsIntegerKind is not enough to interoperate: a binary op
// still requires exact Type equality (see Equal/resolveNumericOperands), so
// an i32 and a u32 no more mix implicitly than an i32 and an i64.
func (t Type) IsIntegerKind() bool {
	switch t.Kind {
	case TypeI8,
		TypeI16,
		TypeI32,
		TypeI64,
		TypeU8,
		TypeU16,
		TypeU32,
		TypeU64,
		TypeUntypedInt:
		return true
	default:
		return false
	}
}

// IsUnsigned reports whether t is an unsigned integer type (u8/u16/u32/u64) -
// codegen uses this to pick unsigned instructions (udiv/urem/zext/unsigned
// compares) over their signed defaults.
func (t Type) IsUnsigned() bool {
	switch t.Kind {
	case TypeU8,
		TypeU16,
		TypeU32,
		TypeU64:
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
	case TypeI8, TypeU8:
		return 8
	case TypeI16, TypeU16:
		return 16
	case TypeI32, TypeU32:
		return 32
	case TypeI64, TypeU64:
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
	case TypeEnum:
		return t.Enum == u.Enum
	case TypeArray:
		if t.Dynamic != u.Dynamic {
			return false
		}
		if !t.Dynamic && t.Size != u.Size {
			return false
		}
		return t.Elem.Equal(*u.Elem)
	case TypePointer:
		return t.Elem.Equal(*u.Elem)
	case TypeMap:
		return t.Key.Equal(*u.Key) && t.Elem.Equal(*u.Elem)
	case TypeFunc, TypeCFunc:
		if len(t.Params) != len(u.Params) {
			return false
		}
		for i := range t.Params {
			if !t.Params[i].Equal(u.Params[i]) {
				return false
			}
		}
		return t.Return.Equal(*u.Return)
	case TypeMultiReturn:
		if len(t.Params) != len(u.Params) {
			return false
		}
		for i := range t.Params {
			if !t.Params[i].Equal(u.Params[i]) {
				return false
			}
		}
		return true
	case TypeGenerator:
		return t.Elem.Equal(*u.Elem)
	case TypeCoroutine:
		return t.Elem.Equal(*u.Elem)
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
	case TypeU8:
		return "u8"
	case TypeU16:
		return "u16"
	case TypeU32:
		return "u32"
	case TypeU64:
		return "u64"
	case TypeF32:
		return "f32"
	case TypeF64:
		return "f64"
	case TypeString:
		return "string"
	case TypeCString:
		return "cstring"
	case TypeBool:
		return "bool"
	case TypeStruct:
		if t.Struct == nil {
			return "<struct>"
		}
		return t.Struct.Symbol.Name
	case TypeEnum:
		if t.Enum == nil {
			return "<enum>"
		}
		return t.Enum.Symbol.Name
	case TypeArray:
		if t.Dynamic {
			return "[]" + t.Elem.String()
		}
		return fmt.Sprintf("[%d]%s", t.Size, t.Elem.String())
	case TypePointer:
		return "*" + t.Elem.String()
	case TypeMap:
		return "map[" + t.Key.String() + "]" + t.Elem.String()
	case TypeFunc:
		return funcTypeString("func", t)
	case TypeCFunc:
		return funcTypeString("cfunc", t)
	case TypeUntypedInt:
		return "untyped int"
	case TypeUntypedFloat:
		return "untyped float"
	case TypeUntypedNil:
		return "nil"
	case TypeMultiReturn:
		parts := make([]string, len(t.Params))
		for i, p := range t.Params {
			parts[i] = p.String()
		}
		return "(" + strings.Join(parts, ", ") + ")"
	case TypeGenerator:
		return "yield " + t.Elem.String()
	case TypeCoroutine:
		if t.Elem == nil || t.Elem.Kind == TypeVoid {
			return "coroutine"
		}
		return "coroutine " + t.Elem.String()
	default:
		return "<unknown type>"
	}
}

// funcTypeString renders a TypeFunc/TypeCFunc's shared "keyword(params)
// [return]" shape - the two kinds' own String cases differ only in which
// keyword introduces the signature.
func funcTypeString(keyword string, t Type) string {
	parts := make([]string, len(t.Params))
	for i, p := range t.Params {
		parts[i] = p.String()
	}
	s := keyword + "(" + strings.Join(parts, ", ") + ")"
	if t.Return != nil && t.Return.Kind != TypeVoid {
		s += " " + t.Return.String()
	}
	return s
}
