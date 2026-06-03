import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
// vscode-oniguruma and vscode-textmate are CommonJS; default-import their
// module.exports so the named functions are reachable under ESM.
import oniguruma from 'vscode-oniguruma';
import vsctm from 'vscode-textmate';

// This tokenizes a sample with the same TextMate engine VS Code uses
// (vscode-textmate + oniguruma), so it verifies the generated cold-start grammar
// produces sane lexical scopes. The accurate classification (declared name vs
// type vs reference) comes from the server's semantic tokens, tested in Go.

const dir = path.dirname(fileURLToPath(import.meta.url));
const root = path.join(dir, '..');
const grammarPath = path.join(root, 'syntaxes', 'masterbelt.tmLanguage.json');

async function loadGrammar() {
  const wasm = fs.readFileSync(path.join(root, 'node_modules', 'vscode-oniguruma', 'release', 'onig.wasm'));
  await oniguruma.loadWASM(wasm);
  const registry = new vsctm.Registry({
    onigLib: Promise.resolve({
      createOnigScanner: (patterns) => new oniguruma.OnigScanner(patterns),
      createOnigString: (s) => new oniguruma.OnigString(s),
    }),
    loadGrammar: async (scope) =>
      scope === 'source.masterbelt'
        ? vsctm.parseRawGrammar(fs.readFileSync(grammarPath, 'utf8'), grammarPath)
        : null,
  });
  const grammar = await registry.loadGrammar('source.masterbelt');
  assert.ok(grammar, 'grammar source.masterbelt loaded');
  return grammar;
}

function tokenize(grammar, source) {
  let stack = vsctm.INITIAL;
  return source.split('\n').map((line) => {
    const result = grammar.tokenizeLine(line, stack);
    stack = result.ruleStack;
    return { line, tokens: result.tokens };
  });
}

// scopesOf returns the scopes of the token covering the first occurrence of
// substr across all lines.
function scopesOf(lines, substr) {
  for (const { line, tokens } of lines) {
    const idx = line.indexOf(substr);
    if (idx < 0) continue;
    for (const t of tokens) {
      if (t.startIndex <= idx && idx < t.endIndex) return t.scopes;
    }
  }
  return [];
}

test('grammar assigns the expected scopes', async () => {
  const grammar = await loadGrammar();
  const source = [
    '/// docs',
    'pub const MyConst: int64 = 100 // trailing',
  ].join('\n');
  const lines = tokenize(grammar, source);

  const cases = [
    ['const', 'keyword.other'],
    ['pub', 'keyword.other'],
    ['MyConst', 'variable.other'],
    ['int64', 'variable.other'],
    ['100', 'constant.numeric'],
    ['///', 'comment.line.documentation'],
    ['// trailing', 'comment.line.double-slash'],
    ['=', 'keyword.operator'],
  ];

  for (const [substr, want] of cases) {
    const scopes = scopesOf(lines, substr);
    assert.ok(
      scopes.some((scope) => scope.includes(want)),
      `${JSON.stringify(substr)} -> ${JSON.stringify(scopes)} should include ${want}`,
    );
  }
});
