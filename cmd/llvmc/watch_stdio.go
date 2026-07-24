package main

/*
#include <stdio.h>

void llvmcSetStdoutUnbuffered(void) {
	setvbuf(stdout, NULL, _IONBF, 0);
}
*/
import "C"

// setStdoutUnbuffered makes libc printf flush immediately. Needed under
// -watch so print output is visible while the process keeps running (piped
// stdout is fully buffered by default).
func setStdoutUnbuffered() {
	C.llvmcSetStdoutUnbuffered()
}
