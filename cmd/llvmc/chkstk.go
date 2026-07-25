package main

/*
extern void ___chkstk_ms(void);

void *llvmcChkstkMsAddr(void) {
	return (void *)___chkstk_ms;
}
*/
import "C"

// chkstkMsAddr returns the real, already-linked-in address of MinGW's
// ___chkstk_ms stack-probe helper (statically pulled from libgcc into this
// very binary the moment any compilation unit references it, same as this
// file does) - see bindMinGWMainThunk's own doc comment (main.go) for why a
// JIT'd function needing it can't resolve it any other way.
func chkstkMsAddr() uintptr {
	return uintptr(C.llvmcChkstkMsAddr())
}
