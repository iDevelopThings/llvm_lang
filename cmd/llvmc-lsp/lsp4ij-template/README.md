# llvmc-lsp - JetBrains (LSP4IJ) template

A [User-defined language server template](https://github.com/redhat-developer/lsp4ij/blob/main/docs/UserDefinedLanguageServerTemplate.md)
for the [LSP4IJ](https://plugins.jetbrains.com/plugin/23257-lsp4ij) plugin, wiring `.llx` files up to
[`cmd/llvmc-lsp`](../main.go) - this project's own diagnostics/hover/go-to-definition/semantic-highlighting
server (see [`src/lsp`](../../../src/lsp/doc.go)).

## Prerequisites

- [LSP4IJ](https://plugins.jetbrains.com/plugin/23257-lsp4ij) installed in your JetBrains IDE (GoLand, IntelliJ, ...).
- Open **this repository** (`llvm_lang`) as the IDE project - the template's command references
  `$PROJECT_DIR$`, so it needs to resolve to this repo's own root.
- Go on `PATH`. **No mingw64/cgo toolchain is needed** for this specific binary, unlike `llvmc.exe` itself -
  `cmd/llvmc-lsp` deliberately never imports `src/codegen`/`tinygo.org/x/go-llvm` (see `src/lsp/doc.go`), so
  `go build ./cmd/llvmc-lsp` works with a plain Go install alone.

## Installing the template

1. Open the **New Language Server** dialog (the `[+]` above the language-server list in
   `Settings/Preferences -> Languages & Frameworks -> Language Servers`, or from the LSP console's own menu).
2. In the **Template** combo-box, choose **Import from custom template...** and select this directory
   (`cmd/llvmc-lsp/lsp4ij-template`).
3. Click **OK**.

That's it - the dialog is pre-filled with the server name, command, and the `*.llx` file mapping from
[`template.json`](template.json). This directory also declares an [`installer.json`](installer.json): the first
time the server starts, LSP4IJ checks whether `llvmc-lsp.exe` already exists next to the project root and, if
not, runs `go build -o llvmc-lsp.exe ./cmd/llvmc-lsp` automatically before launching it - no manual build step
needed. If you'd rather build it yourself first, run:

```bash
go build -o llvmc-lsp.exe ./cmd/llvmc-lsp
```

(or `.\build.ps1` from the repo root, which builds every binary including this one).

## What you get

Opening any `.llx` file (e.g. under [`examples/`](../../../examples)) should now show:

- live diagnostics as you edit (parser/sema errors, republished on every change),
- hover (symbol kind/name + inferred type),
- go-to-definition (including across files in a multi-file package - see `LANGUAGE.md`'s
  "Multi-file packages" section),
- semantic highlighting (keywords, types, functions, struct fields, enum variants, ...).

See `src/lsp/doc.go` for what's deliberately out of scope this round (completion, incremental reparsing) and why.

## Non-Windows

This project currently only targets Windows/mingw64 for its compiler proper (see `AGENTS.md`), but `llvmc-lsp`
itself has no such requirement - `template.json`/`installer.json` both include a `default` (non-Windows) command
too, on the assumption that `go build ./cmd/llvmc-lsp` alone is enough anywhere Go itself runs. This hasn't been
tested on Linux/macOS - if it doesn't work, build `llvmc-lsp` manually and adjust the server command in the
**Server** tab after import.
