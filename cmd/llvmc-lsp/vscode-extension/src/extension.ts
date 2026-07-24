// Minimal VS Code client for llvmc-lsp - exists purely to cross-check
// llvmc-lsp's actual protocol behavior against a second real LSP client,
// independent of LSP4IJ (see ../lsp4ij-template) - useful whenever a bug
// report's root cause is ambiguous between "the server sent the wrong
// data" and "this one client is rendering correct data incorrectly" (see
// the reported foldingRange investigation this extension was built for).
// Not published, not meant for real day-to-day editing - see README.md.
import * as path from "path";
import { ExtensionContext, window, workspace } from "vscode";
import {
	LanguageClient,
	LanguageClientOptions,
	ServerOptions,
	TransportKind,
} from "vscode-languageclient/node";

let client: LanguageClient | undefined;

export function activate(context: ExtensionContext): void {
	const config = workspace.getConfiguration("llvmcLsp");
	let serverPath = config.get<string>("serverPath");

	if (!serverPath) {
		const folders = workspace.workspaceFolders;
		if (folders && folders.length > 0) {
			serverPath = path.join(folders[0].uri.fsPath, "llvmc-lsp.exe");
		}
	}
	if (!serverPath) {
		window.showErrorMessage(
			'llvmc-lsp: no server path configured and no workspace folder open - set "llvmcLsp.serverPath" in settings, or open the llvm_lang repo (which builds llvmc-lsp.exe at its root) as your workspace.',
		);
		return;
	}

	const serverOptions: ServerOptions = {
		command: serverPath,
		transport: TransportKind.stdio,
	};

	const clientOptions: LanguageClientOptions = {
		documentSelector: [{ scheme: "file", language: "llx" }],
	};

	client = new LanguageClient(
		"llvmcLsp",
		"llvmc-lsp",
		serverOptions,
		clientOptions,
	);
	client.start();
}

export function deactivate(): Thenable<void> | undefined {
	if (!client) {
		return undefined;
	}
	return client.stop();
}
