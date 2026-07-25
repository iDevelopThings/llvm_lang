# build.ps1 - convenience build for llvm_lang against system LLVM 22 (MSYS2 mingw64).
#
# The LLVM cgo flags now live in third_party/go-llvm/llvm_config_llvm22.go
# (a local `replace` copy with a `windows` #cgo block added), so the ONLY thing
# needed is the version build tag. No CGO_* env vars, no byollvm.
#
# Builds both real entry points: the compiler CLI (cmd/llvmc -> llvmc.exe)
# and the language server (cmd/llvmc-lsp -> llvmc-lsp.exe). The root package
# itself is not a build target - see its own doc comment.
#
# GoLand can build/run/debug natively too - see SETUP.md. This script is just a
# CLI convenience (e.g. for CI or a quick terminal build).
#
# Usage:
#   .\build.ps1            # build llvmc.exe and llvmc-lsp.exe
#   .\build.ps1 -Run       # build, then run llvmc.exe

param([switch]$Run)
$ErrorActionPreference = "Stop"

# mingw64 must be on PATH for gcc/g++ at build time and libLLVM-22.dll at run time.
$mingw = "C:\msys64\mingw64\bin"
if (-not (Test-Path "$mingw\gcc.exe")) { throw "mingw64 gcc not found in $mingw" }
if ($env:Path -notlike "*$mingw*") { $env:Path = "$mingw;$env:Path" }

go build -tags=llvm22 -o llvmc.exe ./cmd/llvmc
if ($LASTEXITCODE -ne 0) { throw "go build (cmd/llvmc) failed ($LASTEXITCODE)" }
Write-Host "Built llvmc.exe (LLVM 22)" -ForegroundColor Green

go build -tags=llvm22 -o llvmc-lsp.exe ./cmd/llvmc-lsp
if ($LASTEXITCODE -ne 0) { throw "go build (cmd/llvmc-lsp) failed ($LASTEXITCODE)" }
Write-Host "Built llvmc-lsp.exe (LLVM 22)" -ForegroundColor Green

if ($Run) { Write-Host "--- running ---" -ForegroundColor DarkGray; & .\llvmc.exe }
