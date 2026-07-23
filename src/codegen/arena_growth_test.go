package codegen

import (
	"fmt"
	"testing"
)

// This file is the dedicated correctness stress suite for the arena's
// geometric-growth fix (see setupArena's doc comment, runtime.go, and
// DECISIONS.md's dated entry on why arenaChunkSize's fixed-size growth was
// replaced with a doubling `.arena.next_chunk_size` tracker capped at
// arenaChunkMaxSize). Every test here is deliberately scaled to force many
// real chunk-growth events - not just one or two - across as much of the
// geometric progression as practical (including both the *ordinary* doubling
// path and the *oversized* one-off path this fix's design deliberately keeps
// separate), then verifies real byte-for-byte/element-for-element content,
// not just a final length or count. This is the highest-severity class of
// bug this project can ship (silent heap corruption in the one allocator
// every heap-needing feature routes through), so every test below trades
// "fast" for "thorough" wherever the two are in tension - see each test's own
// doc comment for the specific scale/technique tradeoff made and why.

// TestArenaFullChunkProgression drives the arena through its *entire*
// ordinary (non-oversized) geometric progression - every doubling of
// `.arena.next_chunk_size` from the starting 64KiB (arenaChunkSize) up to and
// including the 64MiB cap (arenaChunkMaxSize) - using many small, uniformly
// tiny (16-byte) independent string concatenations, the same shape
// TestArenaGrowsAcrossManyAllocations (string_test.go) already uses at a much
// smaller scale, just large enough here to walk the *entire* series rather
// than clearing the first chunk a couple of times over.
//
// Deliberately NOT one single ever-growing accumulator: each concatenation
// stands alone and is checked immediately, so the *cumulative* fill-up (not
// any single request's own size) is what drives each successive chunk grow -
// this exercises the tracked-baseline-doubling logic specifically, keeping
// every individual request comfortably an "ordinary" (never oversized)
// allocation throughout. Total cumulative bytes needed to walk clean through
// every doubling from 64KiB up to 64MiB is a geometric sum of
// 64KiB*(2^10-1) =~ 63.9MiB (64+128+256+...+32768 KiB) - 4,200,000 iterations
// at 16 bytes each (~64.1MiB total) comfortably clears that, guaranteeing the
// 64MiB cap itself is actually reached (not just approached) at least once.
// This keeps the whole test's real cost linear in the number of allocations
// (a plain per-iteration constant, not quadratic), so despite the large
// iteration count this remains fast - unlike a single naively-`+=`-grown
// string of comparable total size would be (see
// TestArenaLargeStringViaDoubling below for why that test builds its own
// large single string a different way).
func TestArenaFullChunkProgression(t *testing.T) {
	const iterations = 4200000
	src := fmt.Sprintf(`
func build() bool {
	ok := true
	i := 0
	for i < %d {
		a := "12345678" + "ABCDEFGH"
		if a != "12345678ABCDEFGH" {
			ok = false
		}
		i++
	}
	return ok
}
`, iterations)

	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (every concatenation across the full 64KiB->64MiB chunk progression should still be byte-correct)")
	}
}

// TestArenaLargeStringViaDoubling builds one genuinely large (128MiB) single
// string value and verifies its exact length plus real byte content at three
// separate offsets (start, middle, end) via slicing - not just its length -
// catching a pointer/copy-arithmetic bug a length-only check would miss
// entirely (e.g. a correct total size with corrupted or overlapping content
// partway through).
//
// Built via repeated self-concatenation (`s = s + s`, doubling every step)
// rather than a naive `s += "x"`-style per-character loop: growing a single
// accumulator by repeated `+=` costs O(n^2) total bytes copied (every
// intermediate string's own size summed - see DECISIONS.md's dated entry on
// exactly this cost, measured directly on the 50,000-iteration motivating
// benchmark), so reaching 128MiB that way would copy on the order of 10^13
// bytes - minutes, not milliseconds, of pure memcpy work, none of which would
// exercise anything this fix didn't already cover in
// TestArenaFullChunkProgression above. Doubling instead costs O(n) total
// (each step's own cost is proportional to the *current* length, and a
// geometric series of those sums to a constant multiple of the final size),
// reaching the same 128MiB endpoint in only 23 concatenations - fast, while
// still forcing at least one genuinely oversized single allocation once the
// request size itself passes the tracked chunk baseline (deliberately
// exercising the "needsBigger" path setupArena's grow block takes, alongside
// the chunk-progression test above).
//
// The seed pattern is 16 distinct bytes ("0123456789ABCDEF"), chosen
// specifically because doubling a string onto itself is exactly
// self-similar: after any number of doublings, s[i] == seed[i mod 16] for
// every i. That means checking any 16-byte window whose start offset is
// itself a multiple of 16 against the literal seed value is a real,
// deterministic content check - not a weaker "looks plausible" comparison -
// at whichever position along the final 128MiB buffer it's taken.
func TestArenaLargeStringViaDoubling(t *testing.T) {
	const doublings = 23 // 16 * 2^23 = 134,217,728 bytes (128MiB)
	const wantLen = 16 << doublings
	src := fmt.Sprintf(`
func build() bool {
	seed := "0123456789ABCDEF"
	s := seed
	k := 0
	for k < %d {
		s = s + s
		k++
	}

	ok := true
	if len(s) != %d {
		ok = false
	}

	if s[0:16] != seed {
		ok = false
	}

	mid := len(s) / 2
	if s[mid:mid+16] != seed {
		ok = false
	}

	end := len(s) - 16
	if s[end:len(s)] != seed {
		ok = false
	}

	return ok
}
`, doublings, wantLen)

	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (%dMiB doubled string should have exact length %d and byte-correct seed pattern at start/middle/end)", wantLen/(1<<20), wantLen)
	}
}

// TestArenaInterleavedSmallAndLargeAllocations alternates many tiny,
// independent string concatenations with occasional large dynamic-array
// append bursts - exercising the ordinary-doubling path (the tiny
// concatenations) and the oversized one-off path (the array's own backing
// buffer, once its capacity grows past the arena's current tracked chunk
// size) interleaved with each other repeatedly, in combination, rather than
// each in isolation the way the two tests above do.
//
// Both halves are verified deterministically, not just by length/count: the
// string check compares every single concatenation's result against the
// exact literal expected; the array check replays the *exact same* nested
// loop structure used to build it a second time, in the same order, walking
// the already-built array and comparing every element against the exact
// value that was appended for that (i, j) pair - so a bug that corrupted
// even one element out of tens of thousands, anywhere in the array, without
// changing its length, would still be caught.
func TestArenaInterleavedSmallAndLargeAllocations(t *testing.T) {
	src := `
func build() bool {
	ok := true
	bigArr := make([]int, 0)
	i := 0
	for i < 20000 {
		s := "ab" + "cd"
		if s != "abcd" {
			ok = false
		}
		if i % 1000 == 0 {
			j := 0
			for j < 2000 {
				bigArr = append(bigArr, i*10000+j)
				j++
			}
		}
		i++
	}

	pos := 0
	i = 0
	for i < 20000 {
		if i % 1000 == 0 {
			j := 0
			for j < 2000 {
				if bigArr[pos] != i*10000+j {
					ok = false
				}
				pos++
				j++
			}
		}
		i++
	}

	if pos != len(bigArr) {
		ok = false
	}

	return ok
}
`
	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (interleaved small string concats and large array-append bursts should both remain byte/element-correct)")
	}
}

// TestArenaMillionElementArray appends 3,000,000 elements to a single
// dynamic array - forcing the array's own capacity doubling (genAppendCall's
// newcap = max(1, cap*2)) and the arena's own chunk doubling
// (`.arena.next_chunk_size`) to interact repeatedly across many growth events
// on both sides at once - then verifies every single element individually
// (arr[i] == i for every i, not merely len(arr) == 3000000), plus a redundant
// i64 running-sum check against the closed-form triangular-number total.
//
// The i64 accumulator specifically avoids this language's plain `int`
// (i32) - the true sum of 0..2,999,999 is 4,499,998,500,000, far past i32's
// ~2.147483647 billion range, so an i32 accumulator would silently wrap
// before this check ever ran. i64 covers it with enormous headroom
// (max ~9.22e18).
func TestArenaMillionElementArray(t *testing.T) {
	const n = 3000000
	const expectedSum = int64(n) * int64(n-1) / 2 // 4,499,998,500,000
	src := fmt.Sprintf(`
func build() bool {
	arr := make([]int, 0)
	i := 0
	for i < %d {
		arr = append(arr, i)
		i++
	}

	ok := true
	if len(arr) != %d {
		ok = false
	}

	var sum i64 = 0
	i = 0
	for i < %d {
		if arr[i] != i {
			ok = false
		}
		sum += i64(arr[i])
		i++
	}

	var expectedSum i64 = %d
	if sum != expectedSum {
		ok = false
	}

	return ok
}
`, n, n, n, expectedSum)

	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (every one of %d appended elements should read back exactly as its own index, and the i64 sum should match the closed-form total %d)", n, expectedSum)
	}
}

// TestArenaMapChurnAtScale is TestMapChurnTombstonesTriggerGrowth
// (map_test.go) at a much larger scale - insert/remove churn heavy enough to
// force many real hash-table growth events (map buckets are arena-allocated
// too - see CODEGEN.md's "Maps" section), then verifies every still-live key
// reads back with exactly the right value and every removed key is genuinely
// gone, not just that len(m) landed on the right number.
//
// Pattern: insert 20,000 distinct keys, then remove every third one (a real
// tombstone-heavy churn pattern, not a clean insert-only workload), then
// insert 5,000 more brand-new keys on top of that already-churned table.
// Every surviving original key, every removed original key, and every new
// key is checked individually.
func TestArenaMapChurnAtScale(t *testing.T) {
	const initialKeys = 20000
	const newKeys = 5000
	const expectedLen = initialKeys - (initialKeys/3 + 1) + newKeys
	src := fmt.Sprintf(`
func build() bool {
	m := make(map[int]int)
	i := 0
	for i < %d {
		m[i] = i * 3
		i++
	}

	i = 0
	for i < %d {
		if i %% 3 == 0 {
			remove(m, i)
		}
		i++
	}

	i = %d
	for i < %d {
		m[i] = i * 3
		i++
	}

	ok := true

	i = 0
	for i < %d {
		v, present := m[i]
		if i %% 3 == 0 {
			if present {
				ok = false
			}
		} else {
			if !present {
				ok = false
			}
			if v != i*3 {
				ok = false
			}
		}
		i++
	}

	i = %d
	for i < %d {
		v, present := m[i]
		if !present {
			ok = false
		}
		if v != i*3 {
			ok = false
		}
		i++
	}

	if len(m) != %d {
		ok = false
	}

	return ok
}
`, initialKeys, initialKeys, initialKeys, initialKeys+newKeys, initialKeys, initialKeys, initialKeys+newKeys, expectedLen)

	jm := compileAndJIT(t, src)
	if got := jm.runBool(t, "build"); !got {
		t.Errorf("build() = false, want true (every surviving/removed/newly-inserted key across a %d-key insert/remove/insert churn should read back correctly)", initialKeys+newKeys)
	}
}
