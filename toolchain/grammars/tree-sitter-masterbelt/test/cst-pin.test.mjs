import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import {
  beltFiles,
  parseCstSkeleton,
  parseError,
  preorder,
  treeSitterParse,
  normalizeTreeSitter,
  parseSExpr,
} from '../cst-skeleton.mjs';

// The drift killer (C-2 plan §3). The tree-sitter grammar's structural layer is
// hand-written — tree-sitter's own rules cannot be derived from the lexer — so
// nothing stops it from drifting away from the real parser as the language
// grows. This test subordinates it to the real parser: it parses the example
// corpus with the generated tree-sitter parser and checks the result against
// the real parser's concrete syntax tree (the committed *.belt.cst snapshots).
//
// A node-for-node match is neither possible nor the goal — tree-sitter handles
// trivia differently and its node granularity diverges from the CST's. What is
// pinned is the *skeleton*: the pre-order sequence of structural node kinds
// (declarations, statements, expressions, type forms), after both sides are
// normalized to a shared vocabulary (skeleton.mjs). If the real parser starts
// producing a construct the tree-sitter grammar does not model — or models
// differently — the skeletons diverge and CI goes red. The real parser is
// authoritative; the tree-sitter grammar follows.

test('tree-sitter parses the example corpus with no error node', () => {
  for (const { belt, rel } of beltFiles()) {
    const err = parseError(treeSitterParse(belt));
    assert.equal(err, null, `${rel}: tree-sitter parse produced ${err}`);
  }
});

test('tree-sitter skeleton follows the real parser CST', () => {
  const files = beltFiles();
  assert.ok(files.length >= 40, `found ${files.length} example/CST pairs`);
  for (const { belt, cst, rel } of files) {
    const got = preorder(normalizeTreeSitter(parseSExpr(treeSitterParse(belt))));
    const want = preorder(parseCstSkeleton(fs.readFileSync(cst, 'utf8')));
    assert.deepEqual(got, want, `${rel}: tree-sitter skeleton diverges from the real CST`);
  }
});
