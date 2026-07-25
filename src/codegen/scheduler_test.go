package codegen

import "testing"

// This file exercises std/scheduler's own Entry/Scheduler shape (see
// std/scheduler/scheduler.llx) inlined directly into each test source -
// this package's compileSrc/compileAndJITOptimized harness compiles one
// bare tree with no import resolution, so there is no way to `import
// "std:scheduler"` from here (see std/time's own real import-based
// usage instead, examples/time_demo). The worked example under
// examples/scheduler_demo is what actually exercises the real package
// through a genuine import.
const schedulerFixtureSrc = `
struct Entry {
	Handle coroutine
	resumeAt f64
	NextWait f64

	destructor() {
		delete this.Handle
	}
}

struct Scheduler {
	pending []*Entry
	clock f64
}

// ScheduleDelayed only - every test in this file needs precise, explicit
// control over each entry's first resume time (see std/scheduler/scheduler.llx's own
// Schedule/ScheduleDelayed split for the real package's safe default).
func (Scheduler) ScheduleDelayed(e *Entry, initialDelay f64) {
	e.resumeAt = this.clock + initialDelay
	this.pending = append(this.pending, e)
}

func (Scheduler) HasPending() bool {
	return len(this.pending) > 0
}

func (Scheduler) Tick(dt f64) {
	this.clock = this.clock + dt
	write := 0
	i := 0
	for i < len(this.pending) {
		e := this.pending[i]
		if e.resumeAt <= this.clock {
			if resume(e.Handle) {
				e.resumeAt = this.clock + e.NextWait
				this.pending[write] = e
				write = write + 1
			} else {
				delete e
			}
		} else {
			this.pending[write] = e
			write = write + 1
		}
		i = i + 1
	}
	this.pending = this.pending[:write]
}
`

// TestSchedulerZeroPendingTickIsSafe covers Tick against a completely empty
// Scheduler - must not crash, and HasPending stays false before and after.
func TestSchedulerZeroPendingTickIsSafe(t *testing.T) {
	jm := compileAndJITOptimized(t, schedulerFixtureSrc+`
func testZeroPending() bool {
	var sched Scheduler
	before := sched.HasPending()
	sched.Tick(1.0)
	after := sched.HasPending()
	return !before && !after
}
`)
	if got := jm.runBool(t, "testZeroPending"); !got {
		t.Errorf("testZeroPending() = false, want true (no pending entries either side of Tick)")
	}
}

// TestSchedulerEntryNotYetDueIsNotResumed covers an entry whose resumeAt
// hasn't arrived yet - Tick must leave it alone (no resume beyond the
// coroutine's own initial eager run) and it must still be pending.
func TestSchedulerEntryNotYetDueIsNotResumed(t *testing.T) {
	jm := compileAndJITOptimized(t, schedulerFixtureSrc+`
var resumesNotDue int = 0

async func CoroNotDue(NextWait *f64) {
	resumesNotDue = resumesNotDue + 1
	*NextWait = 1.0
	await
	resumesNotDue = resumesNotDue + 1
}

func getResumesNotDue() int { return resumesNotDue }

func testEntryNotYetDue() bool {
	var sched Scheduler
	e := new Entry{}
	e.Handle = CoroNotDue(&e.NextWait)
	sched.ScheduleDelayed(e, 5.0)
	sched.Tick(1.0)
	return sched.HasPending()
}
`)
	if got := jm.runBool(t, "testEntryNotYetDue"); !got {
		t.Errorf("testEntryNotYetDue() = false, want true (entry not due yet, still pending)")
	}
	if got := jm.runInt32(t, "getResumesNotDue"); got != 1 {
		t.Errorf("getResumesNotDue() = %d, want 1 (only the initial eager run, never resumed)", got)
	}
}

// TestSchedulerEntryDueFinishesInOneResume covers an entry whose resumeAt
// has arrived and finishes on that very resume (a single await) - Tick
// must resume it once, then remove and delete it.
func TestSchedulerEntryDueFinishesInOneResume(t *testing.T) {
	jm := compileAndJITOptimized(t, schedulerFixtureSrc+`
var resumesFinishOne int = 0

async func CoroFinishOne(NextWait *f64) {
	resumesFinishOne = resumesFinishOne + 1
	*NextWait = 1.0
	await
	resumesFinishOne = resumesFinishOne + 1
}

func getResumesFinishOne() int { return resumesFinishOne }

func testEntryDueFinishesOneResume() bool {
	var sched Scheduler
	e := new Entry{}
	e.Handle = CoroFinishOne(&e.NextWait)
	sched.ScheduleDelayed(e, 1.0)
	sched.Tick(1.0)
	return sched.HasPending()
}
`)
	if got := jm.runBool(t, "testEntryDueFinishesOneResume"); got {
		t.Errorf("testEntryDueFinishesOneResume() = true, want false (entry finished, must be removed)")
	}
	if got := jm.runInt32(t, "getResumesFinishOne"); got != 2 {
		t.Errorf("getResumesFinishOne() = %d, want 2 (eager run + one resume)", got)
	}
}

// TestSchedulerMultipleResumesDifferentNextWait covers an entry needing
// several resumes across several Ticks, writing a DIFFERENT NextWait before
// each of its own awaits - proving Tick reads NextWait fresh every time
// (not just once) and reschedules relative to the clock at resume time.
func TestSchedulerMultipleResumesDifferentNextWait(t *testing.T) {
	jm := compileAndJITOptimized(t, schedulerFixtureSrc+`
var schedD Scheduler
var resumesD int = 0

async func CoroD(NextWait *f64) {
	resumesD = resumesD + 1
	*NextWait = 1.0
	await
	resumesD = resumesD + 1
	*NextWait = 2.0
	await
	resumesD = resumesD + 1
}

func getResumesD() int { return resumesD }
func pendingD() bool { return schedD.HasPending() }

func setupD() {
	e := new Entry{}
	e.Handle = CoroD(&e.NextWait)
	schedD.ScheduleDelayed(e, 1.0)
}

// clock after each call: 0.5, 1.0, 2.0, 3.0 - the first resume fires at
// clock 1.0 (initial delay), writes NextWait=2.0, rescheduling for
// clock 1.0+2.0=3.0, where the second (finishing) resume fires.
func tickD_a() { schedD.Tick(0.5) }
func tickD_b() { schedD.Tick(0.5) }
func tickD_c() { schedD.Tick(1.0) }
func tickD_d() { schedD.Tick(1.0) }
`)
	jm.runInt32(t, "setupD")
	jm.runInt32(t, "tickD_a") // clock=0.5, not due (resumeAt=1.0)
	if got := jm.runInt32(t, "getResumesD"); got != 1 {
		t.Errorf("after tickD_a: getResumesD() = %d, want 1 (not due yet)", got)
	}
	jm.runInt32(t, "tickD_b") // clock=1.0, due -> resume, NextWait=2.0, new resumeAt=1.0+2.0=3.0
	if got := jm.runInt32(t, "getResumesD"); got != 2 {
		t.Errorf("after tickD_b: getResumesD() = %d, want 2 (first resume happened)", got)
	}
	jm.runInt32(t, "tickD_c") // clock=2.0, not due (resumeAt=3.0)
	if got := jm.runInt32(t, "getResumesD"); got != 2 {
		t.Errorf("after tickD_c: getResumesD() = %d, want 2 (still not due)", got)
	}
	jm.runInt32(t, "tickD_d") // clock=3.0, due -> resume -> finishes
	if got := jm.runInt32(t, "getResumesD"); got != 3 {
		t.Errorf("after tickD_d: getResumesD() = %d, want 3 (second resume, finished)", got)
	}
	if got := jm.runBool(t, "pendingD"); got {
		t.Errorf("pendingD() = true, want false (entry finished, must be removed)")
	}
}

// TestSchedulerMultipleSimultaneousDue covers two entries both due in the
// exact same Tick call - both must be resumed within that one call.
func TestSchedulerMultipleSimultaneousDue(t *testing.T) {
	jm := compileAndJITOptimized(t, schedulerFixtureSrc+`
var resumesE1 int = 0
var resumesE2 int = 0

async func CoroE1(NextWait *f64) {
	resumesE1 = resumesE1 + 1
	*NextWait = 1.0
	await
	resumesE1 = resumesE1 + 1
}

async func CoroE2(NextWait *f64) {
	resumesE2 = resumesE2 + 1
	*NextWait = 1.0
	await
	resumesE2 = resumesE2 + 1
}

func getResumesE1() int { return resumesE1 }
func getResumesE2() int { return resumesE2 }

func testMultipleSimultaneousDue() bool {
	var sched Scheduler
	e1 := new Entry{}
	e1.Handle = CoroE1(&e1.NextWait)
	sched.ScheduleDelayed(e1, 2.0)

	e2 := new Entry{}
	e2.Handle = CoroE2(&e2.NextWait)
	sched.ScheduleDelayed(e2, 2.0)

	sched.Tick(2.0)
	return sched.HasPending()
}
`)
	if got := jm.runBool(t, "testMultipleSimultaneousDue"); got {
		t.Errorf("testMultipleSimultaneousDue() = true, want false (both entries finish in the same Tick)")
	}
	if got := jm.runInt32(t, "getResumesE1"); got != 2 {
		t.Errorf("getResumesE1() = %d, want 2", got)
	}
	if got := jm.runInt32(t, "getResumesE2"); got != 2 {
		t.Errorf("getResumesE2() = %d, want 2", got)
	}
}

// TestSchedulerRemovalDoesNotDisturbOthers covers three simultaneously-due
// entries where one (F1) finishes and is removed while the other two
// (F2, F3) still need a further resume - proving Tick's own in-place
// compaction (write-index shift, then truncating reslice) doesn't corrupt
// a neighboring still-pending entry's own handle/timing, and that a
// removed entry is never mistakenly resumed again on a later Tick.
func TestSchedulerRemovalDoesNotDisturbOthers(t *testing.T) {
	jm := compileAndJITOptimized(t, schedulerFixtureSrc+`
var schedF Scheduler
var resumesF1 int = 0
var resumesF2 int = 0
var resumesF3 int = 0

async func CoroF1(NextWait *f64) {
	resumesF1 = resumesF1 + 1
	*NextWait = 1.0
	await
	resumesF1 = resumesF1 + 1
}

async func CoroF2(NextWait *f64) {
	resumesF2 = resumesF2 + 1
	*NextWait = 1.0
	await
	resumesF2 = resumesF2 + 1
	*NextWait = 1.0
	await
	resumesF2 = resumesF2 + 1
}

async func CoroF3(NextWait *f64) {
	resumesF3 = resumesF3 + 1
	*NextWait = 1.0
	await
	resumesF3 = resumesF3 + 1
	*NextWait = 1.0
	await
	resumesF3 = resumesF3 + 1
}

func getResumesF1() int { return resumesF1 }
func getResumesF2() int { return resumesF2 }
func getResumesF3() int { return resumesF3 }
func pendingF() bool { return schedF.HasPending() }

func setupF() {
	e1 := new Entry{}
	e1.Handle = CoroF1(&e1.NextWait)
	schedF.ScheduleDelayed(e1, 1.0)

	e2 := new Entry{}
	e2.Handle = CoroF2(&e2.NextWait)
	schedF.ScheduleDelayed(e2, 1.0)

	e3 := new Entry{}
	e3.Handle = CoroF3(&e3.NextWait)
	schedF.ScheduleDelayed(e3, 1.0)
}

func tickF1() { schedF.Tick(1.0) }
func tickF2() { schedF.Tick(1.0) }
`)
	jm.runInt32(t, "setupF")
	jm.runInt32(t, "tickF1") // clock=1: all three due - F1 finishes+removed, F2/F3 resumed once
	if got := jm.runInt32(t, "getResumesF1"); got != 2 {
		t.Errorf("after tickF1: getResumesF1() = %d, want 2 (finished)", got)
	}
	if got := jm.runInt32(t, "getResumesF2"); got != 2 {
		t.Errorf("after tickF1: getResumesF2() = %d, want 2 (one resume, still pending)", got)
	}
	if got := jm.runInt32(t, "getResumesF3"); got != 2 {
		t.Errorf("after tickF1: getResumesF3() = %d, want 2 (one resume, still pending)", got)
	}
	if got := jm.runBool(t, "pendingF"); !got {
		t.Errorf("after tickF1: pendingF() = false, want true (F2/F3 still pending)")
	}

	jm.runInt32(t, "tickF2") // clock=2: F2/F3 due again - both finish; F1 must NOT be touched again
	if got := jm.runInt32(t, "getResumesF1"); got != 2 {
		t.Errorf("after tickF2: getResumesF1() = %d, want 2 (removed entry must never resume again)", got)
	}
	if got := jm.runInt32(t, "getResumesF2"); got != 3 {
		t.Errorf("after tickF2: getResumesF2() = %d, want 3 (second resume, finished)", got)
	}
	if got := jm.runInt32(t, "getResumesF3"); got != 3 {
		t.Errorf("after tickF2: getResumesF3() = %d, want 3 (second resume, finished)", got)
	}
	if got := jm.runBool(t, "pendingF"); got {
		t.Errorf("after tickF2: pendingF() = true, want false (all entries finished)")
	}
}
