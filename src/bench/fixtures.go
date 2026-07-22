// Package bench holds shared source-text fixtures used by every pipeline
// stage's benchmarks (see src/lexer/bench_test.go, src/parser/bench_test.go,
// src/sema/bench_test.go, src/codegen/bench_test.go,
// src/compiler/bench_test.go). Centralized here - rather than each stage's
// _test.go file embedding its own copy of a large source string, the way
// this project's ordinary feature tests each embed their own small
// per-test snippet - so every stage's benchmark numbers are measured
// against the exact same input and don't silently drift apart from each
// other over time. See BENCHMARKS.md for the numbers these fixtures
// produced and the reasoning behind picking this particular feature mix.
package bench

import (
	"strconv"
	"strings"
)

// Small is one medium-sized, feature-representative llvm_lang program: a
// struct with a constructor plus a mutating and a non-mutating method,
// recursive function calls, both `for` forms plus `if`/`else`, a dynamic
// array (make/append/len/indexing), a closure, and a pointer (&/*, new/
// delete). This is deliberately not a trivial "hello world" - see
// AGENTS.md's benchmarking task for why a representative feature mix
// matters more than a minimal one for these numbers to mean anything.
const Small = `
struct Point {
	x int
	y int

	constructor(px int, py int) {
		this.x = px
		this.y = py
	}
}

func (Point) sum() int {
	return this.x + this.y
}

func (Point) move(dx int, dy int) {
	this.x = this.x + dx
	this.y = this.y + dy
}

func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

func makeCounter() func() int {
	count := 0
	increment := func() int {
		count = count + 1
		return count
	}
	return increment
}

func swap(a *int, b *int) {
	tmp := *a
	*a = *b
	*b = tmp
}

func sumSlice(s []int) int {
	total := 0
	for i := 0; i < len(s); i++ {
		total += s[i]
	}
	return total
}

func main() int {
	p := Point(1, 2)
	p.move(3, 4)
	print(p.sum()) // (1+3)+(2+4) = 10

	total := 0
	for i := 0; i < 10; i++ {
		total += i * i
	}
	print(total) // 285

	if total > 100 {
		print("big")
	} else {
		print("small")
	}

	s := make([]int, 3)
	s[0] = 1
	s[1] = 2
	s[2] = 3
	s = append(s, 4)
	s = append(s, 5)
	print(sumSlice(s)) // 15

	next := makeCounter()
	print(next()) // 1
	print(next()) // 2
	print(next()) // 3

	x := 10
	y := 20
	swap(&x, &y)
	print(x) // 20
	print(y) // 10

	q := new Point(7, 8)
	print(q.x + q.y) // 15
	delete q

	return fib(10) + total + sumSlice(s) + x + y
}
`

// Large is Small scaled up by mechanically repeating its two "workhorse"
// pieces - the Point struct's arithmetic-heavy methods and the fib/
// sumSlice/swap free functions - many times over under distinct names, each
// called once more from main, to see how each pipeline stage's cost scales
// with input size (see AGENTS.md's benchmarking task: "a second, deliberately
// larger/scaled-up variant ... if that's easy to set up"). Built once at
// package init rather than hand-maintained as a second giant string literal,
// so the repeat count is a single easy-to-change constant.
var Large = buildLarge(40)

// largeRepeatCount is Large's repeat factor - see buildLarge.
const largeRepeatCount = 40

func buildLarge(n int) string {
	var b strings.Builder
	b.WriteString("struct Point {\n\tx int\n\ty int\n\n\tconstructor(px int, py int) {\n\t\tthis.x = px\n\t\tthis.y = py\n\t}\n}\n\n")
	b.WriteString("func (Point) sum() int {\n\treturn this.x + this.y\n}\n\n")
	b.WriteString("func (Point) move(dx int, dy int) {\n\tthis.x = this.x + dx\n\tthis.y = this.y + dy\n}\n\n")

	for i := 0; i < n; i++ {
		b.WriteString(strings.ReplaceAll(`
func fibNAME(n int) int {
	if n < 2 {
		return n
	}
	return fibNAME(n-1) + fibNAME(n-2)
}

func makeCounterNAME() func() int {
	count := 0
	increment := func() int {
		count = count + 1
		return count
	}
	return increment
}

func swapNAME(a *int, b *int) {
	tmp := *a
	*a = *b
	*b = tmp
}

func sumSliceNAME(s []int) int {
	total := 0
	for i := 0; i < len(s); i++ {
		total += s[i]
	}
	return total
}
`, "NAME", strconv.Itoa(i)))
	}

	b.WriteString("func main() int {\n\tacc := 0\n")
	for i := 0; i < n; i++ {
		name := strconv.Itoa(i)
		b.WriteString("\tp" + name + " := Point(" + name + ", " + name + ")\n")
		b.WriteString("\tp" + name + ".move(1, 2)\n")
		b.WriteString("\tacc += p" + name + ".sum()\n")
		b.WriteString("\tacc += fib" + name + "(10)\n")

		b.WriteString("\ts" + name + " := make([]int, 3)\n")
		b.WriteString("\ts" + name + "[0] = 1\n")
		b.WriteString("\ts" + name + "[1] = 2\n")
		b.WriteString("\ts" + name + "[2] = 3\n")
		b.WriteString("\ts" + name + " = append(s" + name + ", 4)\n")
		b.WriteString("\tacc += sumSlice" + name + "(s" + name + ")\n")

		b.WriteString("\tnext" + name + " := makeCounter" + name + "()\n")
		b.WriteString("\tacc += next" + name + "()\n")

		b.WriteString("\tx" + name + " := " + name + "\n")
		b.WriteString("\ty" + name + " := " + name + "\n")
		b.WriteString("\tswap" + name + "(&x" + name + ", &y" + name + ")\n")
		b.WriteString("\tacc += x" + name + " + y" + name + "\n")

		b.WriteString("\tfor i := 0; i < 5; i++ {\n\t\tacc += i\n\t}\n")
	}
	b.WriteString("\treturn acc\n}\n")

	return b.String()
}
