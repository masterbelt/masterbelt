import * as vscode from 'vscode';
import {
  LanguageClient,
  TransportKind,
  type LanguageClientOptions,
  type ServerOptions,
} from 'vscode-languageclient/node';

let client: LanguageClient | undefined;

// activate launches the masterbelt language server (`masterbelt lsp`) and wires
// it to VS Code. Diagnostics, document symbols, and formatting are all served by
// the language server; syntax highlighting comes from the bundled TextMate
// grammar and needs no server.
export function activate(_context: vscode.ExtensionContext): void {
  const settings = vscode.workspace.getConfiguration('masterbelt');
  const command = settings.get<string>('server.path', 'masterbelt');

  const serverOptions: ServerOptions = {
    command,
    args: ['lsp'],
    transport: TransportKind.stdio,
  };

  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: 'file', language: 'masterbelt' }],
  };

  // The client id "masterbelt" makes it read the `masterbelt.trace.server`
  // setting automatically.
  client = new LanguageClient('masterbelt', 'masterbelt', serverOptions, clientOptions);
  void client.start();
}

export function deactivate(): Thenable<void> | undefined {
  return client?.stop();
}
