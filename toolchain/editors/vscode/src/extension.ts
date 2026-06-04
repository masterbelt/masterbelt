import * as vscode from 'vscode';
import {
  LanguageClient,
  type LanguageClientOptions,
  type ServerOptions,
} from 'vscode-languageclient/node';

let client: LanguageClient | undefined;

// activate launches the masterbelt language server (`masterbelt lsp`) and wires
// it to VS Code. Diagnostics, document symbols, formatting, and semantic-token
// highlighting are served by the language server; the TextMate grammar provides
// cold-start colouring and needs no server.
export function activate(context: vscode.ExtensionContext): void {
  const output = vscode.window.createOutputChannel('masterbelt');
  context.subscriptions.push(output);

  startClient(output);

  // Restart re-reads the configuration and spawns a fresh server process —
  // the way to pick up a rebuilt binary or a changed server path without
  // reloading the window.
  context.subscriptions.push(
    vscode.commands.registerCommand('masterbelt.restartServer', async () => {
      output.appendLine('restarting language server...');
      if (client) {
        await client.stop().catch((err: unknown) => {
          output.appendLine(`stopping the old server failed: ${String(err)}`);
        });
        client = undefined;
      }
      startClient(output);
    }),
  );
}

// startClient builds a client from the current configuration and starts it,
// reporting a failed launch clearly (a stale binary without the `lsp`
// subcommand exits immediately, which otherwise surfaces only as a cryptic
// EPIPE).
function startClient(output: vscode.OutputChannel): void {
  const settings = vscode.workspace.getConfiguration('masterbelt');
  // MASTERBELT_SERVER_PATH lets the F5 launch config point at a freshly built
  // binary regardless of which folder the development host opens (see the
  // repo-root .vscode/launch.json); otherwise fall back to the setting, then PATH.
  const command =
    process.env.MASTERBELT_SERVER_PATH || settings.get<string>('server.path', 'masterbelt');
  output.appendLine(`starting language server: ${command} lsp`);

  // Do NOT set `transport: TransportKind.stdio`: for an Executable that makes
  // vscode-languageclient append a `--stdio` flag to the args, which our server
  // does not expect. Omitting it communicates over stdin/stdout with no flag.
  const serverOptions: ServerOptions = {
    command,
    args: ['lsp'],
  };

  const clientOptions: LanguageClientOptions = {
    documentSelector: [{ scheme: 'file', language: 'masterbelt' }],
    // Route the client's logs, trace, and the server's stderr to one channel.
    outputChannel: output,
  };

  // The client id "masterbelt" makes it read the `masterbelt.trace.server`
  // setting automatically.
  client = new LanguageClient('masterbelt', 'masterbelt', serverOptions, clientOptions);
  client.start().catch((err: unknown) => {
    output.appendLine(`language server failed to start: ${String(err)}`);
    void vscode.window.showErrorMessage(
      `masterbelt: could not start the language server "${command}". ` +
        'Build it with `make build`, and set "masterbelt.server.path" if it is not on PATH.',
    );
  });
}

export function deactivate(): Thenable<void> | undefined {
  return client?.stop();
}
