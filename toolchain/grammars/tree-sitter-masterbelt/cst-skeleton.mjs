// Shared logic for the CST-pin test (cst-pin.test.mjs): parse a tree-sitter
// parse tree and a real-parser CST snapshot into one shared "skeleton"
// vocabulary so the two can be compared. See cst-pin.test.mjs for why.

import * as fs from 'node:fs';
import * as path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';

export const pkgDir = path.dirname(fileURLToPath(import.meta.url)); // the package
const repoRoot = path.join(pkgDir, '..', '..', '..');
export const beltDir = path.join(repoRoot, 'pkg', 'belt', 'testdata', 'examples');
export const cstDir = path.join(repoRoot, 'pkg', 'belt', 'parser', 'concrete', 'testdata', 'examples');
const treeSitterBin = path.join(pkgDir, 'node_modules', '.bin', 'tree-sitter');

// treeSitterLeaf names the tree-sitter nodes that correspond to a CST *token*
// (a leaf), not a CST node. The CST snapshot keeps only nodes, so these drop
// with their subtree (a string's escape children are inside the token; an
// identifier has no structural content).
const treeSitterLeaf = new Set([
  'identifier', 'integer', 'string', 'datetime', 'duration', 'escape_sequence',
  'line_comment', 'doc_comment', 'block_comment',
]);

// treeSitterElide names tree-sitter wrapper nodes the CST does not have: their
// structural children are promoted into the parent (a call's arguments are
// direct children of the call in the CST; an effect or wildcard contributes no
// CST node at all and has no structural child to promote).
const treeSitterElide = new Set(['arguments', 'effect']);

// treeSitterAlias maps a tree-sitter node kind to the CST kind it stands for
// when the mechanical snake_case -> PascalCase rule does not (or when two
// tree-sitter forms collapse to one CST kind: the self and null types are both
// a TypeName in the CST).
const treeSitterAlias = {
  source_file: 'File',
  value_ref: 'NameRef',
  func_literal: 'FuncLit',
  record_literal: 'RecordLit',
  collection_literal: 'CollectionLit',
  self_expr: 'SelfExpr',
  null_type: 'TypeName',
  self_type: 'TypeName',
};

function pascal(name) {
  return name
    .split('_')
    .map((p) => p.charAt(0).toUpperCase() + p.slice(1))
    .join('');
}

function cstKind(name) {
  return treeSitterAlias[name] ?? pascal(name);
}

// parseSExpr turns `tree-sitter parse` output into a tree of { kind, children },
// dropping the [row, col] ranges and the `field:` labels.
export function parseSExpr(text) {
  let i = 0;
  function parseNode() {
    while (i < text.length && /\s/.test(text[i])) i++;
    i++; // consume '('
    while (i < text.length && /\s/.test(text[i])) i++;
    const start = i;
    while (i < text.length && /[A-Za-z0-9_]/.test(text[i])) i++;
    const kind = text.slice(start, i);
    const children = [];
    for (;;) {
      while (i < text.length && /\s/.test(text[i])) i++;
      if (text[i] === ')') {
        i++;
        break;
      }
      if (text[i] === '[') {
        while (i < text.length && text[i] !== ']') i++;
        i++;
        continue;
      }
      if (text[i] === '-') {
        i++;
        continue;
      }
      if (text[i] === '(') {
        children.push(parseNode());
        continue;
      }
      const s = i;
      while (i < text.length && /[A-Za-z0-9_]/.test(text[i])) i++;
      if (text[i] === ':') {
        i++;
        continue;
      }
      if (i === s) i++;
    }
    return { kind, children };
  }
  return parseNode();
}

// normalizeTreeSitter folds a parsed tree-sitter node into the shared skeleton.
export function normalizeTreeSitter(node) {
  const children = node.children.flatMap(normalizeTreeSitter);
  if (treeSitterLeaf.has(node.kind)) return [];
  if (treeSitterElide.has(node.kind)) return children;
  return [{ kind: cstKind(node.kind), children }];
}

// parseCstSkeleton reads a *.belt.cst snapshot's `# tree` section, keeping only
// its nodes (lines with no quoted token text).
export function parseCstSkeleton(text) {
  const lines = text.split('\n');
  const treeStart = lines.indexOf('# tree');
  if (treeStart < 0) throw new Error('snapshot has no # tree section');
  const root = { kind: null, children: [], depth: -1 };
  const stack = [root];
  for (let n = treeStart + 1; n < lines.length; n++) {
    const line = lines[n];
    if (line.startsWith('#')) break; // # diagnostics
    if (line.trim() === '') continue;
    const indent = line.length - line.trimStart().length;
    const depth = indent / 2;
    const content = line.trim();
    // A token leaf is `Kind "text"`; a node is a bare `Kind`. Drop leaves.
    if (/ "/.test(content) || content.endsWith('""')) continue;
    const node = { kind: content, children: [], depth };
    while (stack.length && stack[stack.length - 1].depth >= depth) stack.pop();
    stack[stack.length - 1].children.push(node);
    stack.push(node);
  }
  return root.children;
}

// preorder flattens a skeleton forest into its pre-order kind sequence.
export function preorder(nodes) {
  const out = [];
  for (const node of nodes) {
    out.push(node.kind);
    out.push(...preorder(node.children));
  }
  return out;
}

// beltFiles finds every example source and its matching CST snapshot.
export function beltFiles() {
  const out = [];
  function walk(d) {
    for (const entry of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, entry.name);
      if (entry.isDirectory()) walk(p);
      else if (entry.name.endsWith('.belt')) {
        const rel = path.relative(beltDir, p);
        const cst = path.join(cstDir, rel + '.cst');
        if (fs.existsSync(cst)) out.push({ belt: p, cst, rel });
      }
    }
  }
  walk(beltDir);
  out.sort((a, b) => a.rel.localeCompare(b.rel));
  return out;
}

// parseError returns the first ERROR/MISSING marker in a parse, or null.
export function parseError(sexpr) {
  const m = /\((ERROR|MISSING)\b/.exec(sexpr) || /UNEXPECTED/.exec(sexpr);
  return m ? m[0] : null;
}

// treeSitterParse runs the pinned tree-sitter CLI over a file and returns the
// raw S-expression.
export function treeSitterParse(beltPath) {
  return execFileSync(treeSitterBin, ['parse', beltPath], {
    cwd: pkgDir,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    // Silence the CLI's "no parser directories configured" notice on stderr;
    // the local grammar in pkgDir is found regardless.
    stdio: ['ignore', 'pipe', 'ignore'],
  });
}

// treeSitterSkeleton parses a file and returns its normalized skeleton forest.
export function treeSitterSkeleton(beltPath) {
  return normalizeTreeSitter(parseSExpr(treeSitterParse(beltPath)));
}

// queryCaptures runs a highlight query file over a source and returns the
// { capture, text } pairs `tree-sitter query` reports — the colours the editor
// would paint. It is how the highlight golden checks the generated queries.
export function queryCaptures(queryPath, beltPath) {
  const out = execFileSync(treeSitterBin, ['query', queryPath, beltPath], {
    cwd: pkgDir,
    encoding: 'utf8',
    maxBuffer: 64 * 1024 * 1024,
    stdio: ['ignore', 'pipe', 'ignore'],
  });
  const captures = [];
  const re = /capture: \d+ - ([^,]+), start: \([^)]*\), end: \([^)]*\), text: `(.*)`/g;
  let m;
  while ((m = re.exec(out)) !== null) {
    captures.push({ capture: m[1], text: m[2] });
  }
  return captures;
}
