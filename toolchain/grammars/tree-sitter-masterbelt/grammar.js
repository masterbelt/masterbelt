// The masterbelt tree-sitter grammar — the structural layer.
//
// This file is HAND-WRITTEN. Its lexical layer (the keyword table, operator
// spellings, comment markers, and literal regexes) is required from lexical.js,
// which editorgen generates from pkg/source/token — the same lexer truth the
// TextMate grammar is generated from. Only the structural rules (declarations,
// expressions, statements) live here; they are tree-sitter's own and cannot be
// derived from the lexer, so they are kept honest against the real parser by
// the CST snapshot test (C-2 plan §3), not by being generated.
//
// The structure mirrors the concrete syntax tree of pkg/source/cst as closely
// as tree-sitter allows; node names follow the CST's kinds in snake_case so the
// snapshot comparison can map them.

const lex = require("./lexical.js");
const { kw, op } = lex;

// Operator precedence, lowest to highest. Postfix access (call/member/index)
// binds tightest; the ternary binds loosest.
const PREC = {
  ternary: 1,
  range: 2,
  or: 3,
  and: 4,
  equality: 5,
  comparison: 6,
  additive: 7,
  multiplicative: 8,
  unary: 9,
  await: 10,
  postfix: 11,
};

// commaSep1 / commaSep build comma-separated lists. A trailing comma is allowed
// everywhere the language permits it (record and collection literals, lists).
function commaSep1(rule) {
  return seq(rule, repeat(seq(op.Comma, rule)));
}
function commaSep(rule) {
  return optional(commaSep1(rule));
}

module.exports = grammar({
  name: lex.name,

  // The word token drives keyword extraction: every bare keyword string below
  // is recognised against this rule, so `const` lexes as a keyword and `consts`
  // as an identifier.
  word: ($) => $.identifier,

  // Trivia floats between tokens (the CST attaches it to nodes; the highlight
  // queries colour it). Whitespace, newlines, and the three comment shapes.
  extras: ($) => [/\s/, $.line_comment, $.doc_comment, $.block_comment],

  // The ambiguities tree-sitter resolves with GLR exploration rather than a
  // fixed lookahead. Each pair is genuinely ambiguous at a token boundary and
  // only the surrounding context picks the parse.
  conflicts: ($) => [
    // `Name { ... }` is a record literal; `Name` alone is a value reference.
    // The `{` decides, one token after the identifier.
    [$.value_ref, $.record_literal],
    // `{` opening a statement-position block vs an inferred record literal
    // (`{ x: 1 }`). Only the surrounding context — an expression slot vs a
    // statement slot — tells them apart, and GLR resolves it.
    [$.block, $.record_literal],
  ],

  rules: {
    // A source file is a sequence of top-level declarations.
    source_file: ($) => repeat($._declaration),

    _declaration: ($) =>
      choice(
        $.const_decl,
        $.type_decl,
        $.enum_decl,
        $.interface_decl,
        $.func_decl,
        $.use_decl,
        $.assert_decl,
        $.master_decl,
      ),

    // --- declarations --------------------------------------------------------

    const_decl: ($) =>
      seq(
        optional(kw.pub),
        kw.const,
        field("name", $.identifier),
        optional($.type_clause),
        optional($.initializer),
      ),

    type_decl: ($) =>
      seq(
        optional(kw.pub),
        kw.type,
        field("name", $.identifier),
        optional($.generic_params),
        op.Assign,
        field("type", $._type),
        optional($.where_clause),
        repeat($.impl_block),
      ),

    enum_decl: ($) =>
      seq(
        optional(kw.pub),
        kw.enum,
        field("name", $.identifier),
        optional($.type_clause),
        op.LBrace,
        repeat(seq($.enum_member, optional(op.Comma))),
        op.RBrace,
        repeat($.impl_block),
      ),

    enum_member: ($) =>
      seq(field("name", $.identifier), optional($.initializer)),

    interface_decl: ($) =>
      seq(
        optional(kw.pub),
        kw.interface,
        field("name", $.identifier),
        optional($.generic_params),
        optional($.interface_parents),
        op.LBrace,
        repeat($.interface_member),
        op.RBrace,
      ),

    interface_parents: ($) =>
      seq(op.Colon, commaSep1($.type_name)),

    interface_member: ($) =>
      seq(
        optional(kw.pub),
        field("name", $.identifier),
        optional($.generic_params),
        $.param_list,
        op.Colon,
        field("return", $._type),
        optional($.block),
      ),

    // A top-level function. An extern fn (a builtin-surface native) has no
    // body, so the body is optional; an ordinary fn carries a block or an arrow
    // expression.
    func_decl: ($) =>
      seq(
        optional(kw.pub),
        optional(kw.extern),
        kw.fn,
        repeat($.effect),
        field("name", $.identifier),
        optional($.generic_params),
        $.param_list,
        op.Colon,
        field("return", $._type),
        optional(choice($.block, seq(op.Arrow, field("body", $._expr)))),
      ),

    use_decl: ($) =>
      seq(
        optional(kw.pub),
        kw.use,
        choice($.identifier, $.use_list, op.Star),
        kw.from,
        field("source", $.string),
      ),

    use_list: ($) => seq(op.LBrace, commaSep1($.identifier), op.RBrace),

    assert_decl: ($) => seq(kw.assert, $._expr),

    // --- master declarations -------------------------------------------------

    // master Name { record <type-body> primary <key> }. master/record/primary
    // are context keywords, exactly like the get/set/static modifiers: the
    // lexer leaves them identifiers and the real parser recognises them by
    // position, while here they are extracted as keywords (sound while they are
    // not also used as ordinary names, which they are not in the corpus). Each
    // is wrapped in a master_keyword node to mirror the CST's MasterKeyword.
    //
    // The keyword is pinned per construct (master_keyword for the head, then
    // "record"/"primary" aliased to the same node name in the members) so a
    // primary whose key is a bare identifier — which would also parse as a record
    // body's type — is not ambiguous with the record member.
    master_decl: ($) =>
      seq(
        optional(kw.pub),
        $.master_keyword,
        field("name", $.identifier),
        op.LBrace,
        repeat(choice($.master_record, $.master_primary)),
        op.RBrace,
      ),

    master_keyword: ($) => "master",

    master_record: ($) =>
      seq(
        alias("record", $.master_keyword),
        field("type", $._type),
        optional($.where_clause),
        repeat($.impl_block),
      ),

    master_primary: ($) =>
      seq(
        alias("primary", $.master_keyword),
        choice(
          field("key", $.identifier),
          // A trailing comma is permitted before ")", mirroring the concrete
          // parser, so a composite key under construction is not flagged.
          seq(op.LParen, commaSep1($.identifier), optional(op.Comma), op.RParen),
        ),
      ),

    // --- impl blocks and methods --------------------------------------------

    impl_block: ($) =>
      seq(
        kw.impl,
        optional(field("interface", $.type_name)),
        op.LBrace,
        repeat($._impl_item),
        op.RBrace,
      ),

    _impl_item: ($) => choice($.method_decl, $.const_decl),

    // [pub] [extern] [modifier] [fn] effect* name [generics] params : type [block].
    // A builtin method can carry extern, a modifier, and fn at once
    // (`pub extern static fn nondet now()`), so the markers are independently
    // optional in source order rather than the CST's simplified alternation.
    method_decl: ($) =>
      seq(
        optional(kw.pub),
        optional(kw.extern),
        optional($.modifier),
        optional(kw.fn),
        repeat($.effect),
        field("name", $.identifier),
        optional($.generic_params),
        $.param_list,
        op.Colon,
        field("return", $._type),
        optional($.block),
      ),

    // get / set / static are context keywords: the lexer leaves them
    // identifiers and the real parser recognises them by position. The
    // tree-sitter lexer extracts them as keywords, which is sound as long as
    // they are not also used as ordinary names (they are not, in the corpus).
    modifier: ($) => choice("get", "set", "static"),

    effect: ($) => choice(kw.io, kw.async, kw.nondet),

    // A name position the grammar reads a reserved word as an ordinary
    // identifier: a member after ".", a record field name, a parameter name.
    // It mirrors the real parser's nameLike — every keyword is admissible there
    // because the position begins no keyword construct (item.type, { type: ... },
    // fn(for: int)). The tree-sitter lexer extracts keywords eagerly, so each
    // must be listed explicitly to be accepted here.
    _name: ($) => choice($.identifier, ...Object.values(lex.kw)),

    param_list: ($) => seq(op.LParen, commaSep($.param), op.RParen),

    param: ($) =>
      seq(field("name", $._name), optional(seq(op.Colon, field("type", $._type)))),

    // --- type expressions ----------------------------------------------------

    _type: ($) => choice($.union_type, $._primary_type),

    union_type: ($) =>
      seq($._primary_type, repeat1(seq(op.Pipe, $._primary_type))),

    _primary_type: ($) =>
      choice(
        $.type_name,
        $.record_type,
        $.func_type,
        $.builtin_type,
        $.null_type,
        $.self_type,
        $.type_type,
      ),

    // A named type, optionally qualified by a sibling's namespace (geo.Point)
    // and optionally applied (list<T>). The CST keeps the dotted path flat
    // inside the TypeName. A segment after a dot may be a keyword read as a field
    // name (Schema.type), the same as a member name in value position.
    type_name: ($) =>
      seq($.identifier, repeat(seq(op.Dot, $._name)), optional($.generic_args)),

    null_type: ($) => kw.null,
    self_type: ($) => kw.self,
    // The `type` keyword names the metatype (type : type). It is a builtin type
    // name in type position, the CST's TypeName, admissible because the position
    // never begins the `type Foo =` declaration the keyword otherwise heads.
    type_type: ($) => kw.type,

    generic_params: ($) => seq(op.Lt, commaSep1($.generic_param), op.Gt),
    generic_param: ($) =>
      seq(field("name", $.identifier), optional(seq(op.Colon, field("constraint", $._type)))),
    generic_args: ($) => seq(op.Lt, commaSep1($._type), op.Gt),

    // Fields separate with commas and/or newlines (newlines are trivia, so a
    // field simply abuts the next), with an optional trailing comma.
    record_type: ($) =>
      seq(op.LBrace, repeat(seq($.field, optional(op.Comma))), op.RBrace),
    field: ($) =>
      seq(field("name", $._name), op.Colon, field("type", $._type)),

    // The return type is a primary type: a union return (fn(): A | B) would be
    // ambiguous with a union *of* function types, so it must be parenthesised.
    func_type: ($) =>
      seq(kw.fn, $.param_list, op.Colon, field("return", $._primary_type)),

    builtin_type: ($) => seq(kw.builtin, optional($.generic_args)),

    where_clause: ($) => seq(kw.where, $._expr),

    type_clause: ($) => seq(op.Colon, field("type", $._type)),
    initializer: ($) => seq(op.Assign, field("value", $._expr)),

    // --- statements ----------------------------------------------------------

    block: ($) => seq(op.LBrace, repeat($._statement), op.RBrace),

    _statement: ($) =>
      choice(
        $.return_stmt,
        $.let_stmt,
        $.assign_stmt,
        $.if_stmt,
        $.switch_stmt,
        $.match_stmt,
        $.for_stmt,
      ),

    return_stmt: ($) => seq(kw.return, $._expr),

    // The `= expr` is an initializer node, as in a const declaration (the CST
    // reuses the Initializer kind here).
    let_stmt: ($) =>
      seq(kw.let, field("name", $.identifier), optional($.type_clause), $.initializer),

    assign_stmt: ($) =>
      seq(field("target", $._expr), op.Assign, field("value", $._expr)),

    if_stmt: ($) =>
      seq(
        kw.if,
        field("condition", $._expr),
        $.block,
        optional(seq(kw.else, choice($.if_stmt, $.block))),
      ),

    switch_stmt: ($) =>
      seq(kw.switch, field("value", $._expr), op.LBrace, repeat($.switch_arm), op.RBrace),

    // The wildcard `_` is an ordinary identifier: the lexer does not reserve
    // it, and the real parser reads a switch `_` as a value reference (NameRef)
    // and a match `_` as a member type (TypeName), so it needs no node here.
    switch_arm: ($) =>
      seq(commaSep1($._expr), op.Arrow, choice($.block, $._statement)),

    match_stmt: ($) =>
      seq(kw.match, field("value", $._expr), op.LBrace, repeat($.match_arm), op.RBrace),

    match_arm: ($) =>
      seq($.match_pattern, op.Arrow, choice($.block, $._statement)),

    match_pattern: ($) =>
      seq($._type, optional(field("binding", $.identifier))),

    for_stmt: ($) =>
      seq(
        kw.for,
        field("name", $.identifier),
        choice(kw.of, kw.in),
        field("collection", $._expr),
        $.block,
      ),

    // --- expressions ---------------------------------------------------------

    _expr: ($) =>
      choice(
        $.literal,
        $.value_ref,
        $.self_expr,
        $.binary_expr,
        $.unary_expr,
        $.ternary_expr,
        $.range_expr,
        $.call_expr,
        $.member_expr,
        $.index_expr,
        $.collection_literal,
        $.record_literal,
        $.func_literal,
        $.paren_expr,
        $.await_expr,
      ),

    // A value reference: a name, or the `type` keyword naming the metatype as a
    // value (const t = type). The real parser lowers `type` here to a NameRef,
    // which value_ref aliases to in the CST skeleton.
    value_ref: ($) => choice($.identifier, kw.type),
    self_expr: ($) => kw.self,

    literal: ($) =>
      choice($.integer, $.string, $.datetime, $.duration, kw.true, kw.false, kw.null),

    binary_expr: ($) => {
      const table = [
        [PREC.or, [op.PipePipe]],
        [PREC.and, [op.AmpAmp]],
        [PREC.equality, [op.EqEq, op.BangEq]],
        [PREC.comparison, [op.Lt, op.LtEq, op.Gt, op.GtEq]],
        [PREC.additive, [op.Plus, op.Minus]],
        [PREC.multiplicative, [op.Star, op.Slash, op.Percent]],
      ];
      return choice(
        ...table.flatMap(([precedence, ops]) =>
          ops.map((operator) =>
            prec.left(
              precedence,
              seq(
                field("left", $._expr),
                field("operator", operator),
                field("right", $._expr),
              ),
            ),
          ),
        ),
      );
    },

    unary_expr: ($) =>
      prec(PREC.unary, seq(field("operator", choice(op.Bang, op.Minus, op.Plus)), $._expr)),

    ternary_expr: ($) =>
      prec.right(
        PREC.ternary,
        seq(
          field("condition", $._expr),
          op.Question,
          field("then", $._expr),
          op.Colon,
          field("else", $._expr),
        ),
      ),

    range_expr: ($) =>
      prec.left(
        PREC.range,
        seq(field("start", $._expr), choice(op.DotDot, op.DotDotDot), field("end", $._expr)),
      ),

    call_expr: ($) =>
      prec(PREC.postfix, seq(field("callee", $._expr), $.arguments)),
    arguments: ($) => seq(op.LParen, commaSep($._expr), op.RParen),

    member_expr: ($) =>
      prec(PREC.postfix, seq(field("receiver", $._expr), op.Dot, field("member", $._name))),

    index_expr: ($) =>
      prec(PREC.postfix, seq(field("receiver", $._expr), op.LBracket, $._expr, op.RBracket)),

    paren_expr: ($) => seq(op.LParen, $._expr, op.RParen),

    await_expr: ($) => prec(PREC.await, seq(kw.await, $._expr)),

    collection_literal: ($) =>
      seq(
        op.LBracket,
        commaSep(choice($.map_entry, $._expr)),
        optional(op.Comma),
        op.RBracket,
      ),
    map_entry: ($) => seq(field("key", $._expr), op.Colon, field("value", $._expr)),

    record_literal: ($) =>
      seq(
        optional(field("type", $.identifier)),
        op.LBrace,
        repeat(seq($.record_field, optional(op.Comma))),
        op.RBrace,
      ),
    record_field: ($) =>
      seq(field("name", $._name), op.Colon, field("value", $._expr)),

    func_literal: ($) =>
      seq(
        kw.fn,
        $.param_list,
        optional(seq(op.Colon, field("return", $._type))),
        choice(seq(op.Arrow, field("body", $._expr)), $.block),
      ),

    // --- lexical layer (generated names; rules built from lexical.js) --------

    identifier: ($) => lex.identifier,
    integer: ($) => token(lex.integer),
    // datetime and duration must out-rank a bare integer at the same start; a
    // longer match already wins, and the explicit token() keeps them atomic.
    datetime: ($) => token(lex.datetime),
    duration: ($) => token(lex.duration),

    string: ($) =>
      seq(
        '"',
        repeat(choice($.escape_sequence, token.immediate(/[^"\\]+/))),
        '"',
      ),
    escape_sequence: ($) => token.immediate(lex.escape),

    line_comment: ($) => token(seq(lex.comment.line, /[^\n]*/)),
    doc_comment: ($) => token(prec(1, seq(lex.comment.doc, /[^\n]*/))),
    block_comment: ($) =>
      token(seq(lex.comment.blockOpen, /[^*]*\*+([^/*][^*]*\*+)*/, "/")),
  },
});
