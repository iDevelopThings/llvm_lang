# test.ps1 - convenience test runner for llvm_lang against system LLVM 22 (MSYS2 mingw64).
#
# Mirrors build.ps1's mingw64 PATH setup - required for cgo to build against
# gcc/g++ and for the resulting test binaries to load libLLVM-22.dll at run
# time. Without it, tests that touch src/codegen or cmd/llvmc fail with a
# native crash (0xC0000139 / STATUS_ENTRYPOINT_NOT_FOUND), not a normal Go
# test failure - easy to misdiagnose as a real regression if hand-typed in an
# ad-hoc shell instead.
#
# Usage:
#   .\test.ps1                  # go test -tags=llvm22 ./...
#   .\test.ps1 -Verbose          # add -v
#   .\test.ps1 -Run TestFoo      # add -run=TestFoo
#   .\test.ps1 -Verbose -Run TestFoo ./src/codegen/...

param(
    [switch]$Verbose,
    [string]$Run,
    [string]$Package = "./..."
)
$ErrorActionPreference = "Stop"

$mingw = "C:\msys64\mingw64\bin"
if (-not (Test-Path "$mingw\gcc.exe")) { throw "mingw64 gcc not found in $mingw" }
if ($env:Path -notlike "*$mingw*") { $env:Path = "$mingw;$env:Path" }

$goArgs = @("test", "-tags=llvm22")
if ($Verbose) { $goArgs += "-v" }
if ($Run) { $goArgs += "-run=$Run" }
$goArgs += $Package

go @goArgs
if ($LASTEXITCODE -ne 0) { throw "go test failed ($LASTEXITCODE)" }
Write-Host "Tests passed (LLVM 22)" -ForegroundColor Green
