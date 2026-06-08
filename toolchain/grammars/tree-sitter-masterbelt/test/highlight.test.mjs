import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as path from 'node:path';
import { pkgDir, queryCaptures } from '../cst-skeleton.mjs';

// The highlight golden (C-2 plan §3/§4). It runs the generated default
// highlights.scm (the nvim-treesitter vocabulary) over a fixture exercising
// every category and asserts the captures, so a query that stops colouring a
// construct — or colours it as the wrong category — fails CI. The lexical
// captures (keyword/comment/number/string/operator) double as the check that
// the queries agree with the token table the queries are generated from.

const queryPath = path.join(pkgDir, 'queries', 'highlights.scm');
const samplePath = path.join(import.meta.dirname, 'highlight-sample.belt');

test('the default highlights.scm colours every category', () => {
  const got = queryCaptures(queryPath, samplePath);
  const has = (capture, text) =>
    got.some((c) => c.capture === capture && c.text === text);

  // Lexical layer — the cold-start subset, the same the TextMate grammar paints.
  const lexical = [
    ['keyword', 'pub'],
    ['keyword', 'const'],
    ['keyword', 'fn'],
    ['keyword', 'return'],
    ['comment.documentation', '/// A doc comment.'],
    ['comment', '// a line comment'],
    ['number', '1'],
    ['operator', '='],
    ['operator', ':'],
  ];

  // Structural layer — what tree-sitter colours that TextMate cannot, from the
  // parse context, mirroring the server's semantic tokens.
  const structural = [
    ['type', 'Color'], // a type declaration's name and its uses in type position
    ['type', 'nint'], // a type reference
    ['type', 'Tier'], // an enum declaration's name
    ['function', 'paint'], // a top-level function (declaration and call)
    ['function.method', 'make'], // a method declaration
    ['variable.parameter', 'value'], // a parameter
    ['property', 'channel'], // a record field
    ['constant', 'Bronze'], // an enum member
    ['variable', 'Base'], // a constant's declared name
    ['keyword', 'master'], // the master/record/primary context keywords
    ['keyword', 'record'],
    ['keyword', 'primary'],
    ['type', 'Skill'], // a master declaration's name
    ['property', 'id'], // a primary-key column
  ];

  for (const [capture, text] of [...lexical, ...structural]) {
    assert.ok(has(capture, text), `expected ${JSON.stringify(text)} captured as @${capture}`);
  }
});
