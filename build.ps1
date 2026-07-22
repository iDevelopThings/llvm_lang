# build.ps1 - convenience build for llvm_lang against system LLVM 22 (MSYS2 mingw64).
#
# The LLVM cgo flags now live in third_party/go-llvm/llvm_config_llvm22.go
# (a local `replace` copy with a `windows` #cgo block added), so the ONLY thing
# needed is the version build tag. No CGO_* env vars, no byollvm.
#
# GoLand can build/run/debug natively too - see SETUP.md. This script is just a
# CLI convenience (e.g. for CI or a quick terminal build).
#
# Usage:
#   .\build.ps1            # build llvm_lang.exe
#   .\build.ps1 -Run       # build, then run it

param([switch]$Run)
$ErrorActionPreference = "Stop"

# mingw64 must be on PATH for gcc/g++ at build time and libLLVM-22.dll at run time.
$mingw = "C:\msys64\mingw64\bin"
if (-not (Test-Path "$mingw\gcc.exe")) { throw "mingw64 gcc not found in $mingw" }
if ($env:Path -notlike "*$mingw*") { $env:Path = "$mingw;$env:Path" }

go build -tags=llvm22 -o llvm_lang.exe .
if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }
Write-Host "Built llvm_lang.exe (LLVM 22)" -ForegroundColor Green

if ($Run) { Write-Host "--- running ---" -ForegroundColor DarkGray; & .\llvm_lang.exe }
