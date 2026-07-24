# llvmc-lsp - VS Code dev client

A minimal, unpublished VS Code extension that speaks to
[`cmd/llvmc-lsp`](../main.go) - built specifically to cross-check
llvmc-lsp's actual protocol responses against a **second** real LSP
client, independent of [LSP4IJ](../lsp4ij-template) (the JetBrains
plugin this project also has a template for). Useful whenever a bug
report's root cause is ambiguous between "the server sent the wrong
data" and "this one client is rendering correct data incorrectly" - if
the same source produces different folding/highlighting/etc. behavior in
VS Code vs. JetBrains, that's strong evidence the server side is fine and
the bug is client-specific; if both clients show the identical wrong
behavior, that points back at the server.

This is deliberately not a "real" extension - no syntax highlighting
grammar, no icon, not published to the Marketplace. It exists to run
llvmc-lsp and show you exactly what it does, nothing more.

## Setup

Requires [Node.js](https://nodejs.org/) and npm.

```bash
cd cmd/llvmc-lsp/vscode-extension
npm install
npm run compile
```

Build `llvmc-lsp.exe` at the repo root if you haven't already (no
mingw64/cgo needed for this binary specifically - see `src/lsp/doc.go`):

```bash
go build -o llvmc-lsp.exe ./cmd/llvmc-lsp
```

(or `.\build.ps1` from the repo root, which builds every binary including
this one).

## Running

1. Open **this directory** (`cmd/llvmc-lsp/vscode-extension`) in VS Code.
2. Press **F5** (or Run → Start Debugging). This compiles the extension
   and launches a second "Extension Development Host" VS Code window with
   it active.
3. In that new window, open the `llvm_lang` repo root as a folder (or any
   folder containing `llvmc-lsp.exe`) and open a `.llx` file, e.g.
   `examples/constructors/constructors.llx`.
4. The client looks for `llvmc-lsp.exe` at the open workspace's own root
   by default. If it's somewhere else, set `llvmcLsp.serverPath` in that
   window's own Settings (search "llvmc").

## Watching the wire protocol

`llvmcLsp.trace.server` defaults to `"verbose"` - open **View → Output**,
then pick the **llvmc-lsp** channel from the dropdown to see every
request/response/notification exchanged with the server, exactly like the
JetBrains LSP console.
