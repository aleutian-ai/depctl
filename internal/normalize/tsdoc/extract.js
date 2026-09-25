'use strict';
// extract.js: a small, dependency-free structural scanner for a single
// .d.ts OR plain .js file. Extracts top-level exported declarations
// (function, class + its own public members, interface, type alias,
// const) together with their immediately-preceding JSDoc comment, plus
// the file's own leading module-level doc comment when one exists. No
// npm package required — only plain Node.js. Deliberately syntactic,
// not semantic: no type resolution, no import following, no
// re-export-list resolution ("export { X } from './y'" is silently
// skipped, same as an unparsed statement — see tsdoc.go's own
// Non-goals).
//
// JSDoc fallback (NORM-008's deferred scope, closed here): for plain
// .js input with no .d.ts, recognizes both ES module exports (`export
// function`/`export class`/`export const`, already handled below —
// TypeScript declaration syntax is a strict subset of these forms) and
// CommonJS assignment exports (`exports.foo = ...` / `module.exports.foo
// = ...`), each requiring its own preceding JSDoc block to carry the
// signature/doc information a .d.ts would otherwise supply directly —
// no type-checking, JSDoc's own @param/@returns tags (or lack thereof)
// are passed through as-is in `doc`, never re-parsed or reformatted.
// Deliberately NOT handled (a real, disclosed limitation, matching the
// same "syntactic, not semantic" scope as .d.ts re-exports): whole-
// object-literal exports (`module.exports = { foo, bar }`), since that
// requires resolving which local declaration each shorthand property
// refers to — semantic work, not a structural scan.
//
// Usage: node extract.js <path-to-.d.ts-or-.js>
// Output: one JSON object on stdout,
//   {"doc": "<module doc, if any>", "symbols": [{"kind","name","signature","doc","receiver"?}]}

const fs = require('fs');

const path = process.argv[2];
const text = fs.readFileSync(path, 'utf8');
const lines = text.split(/\r\n|\n/);

function stripDocComment(raw) {
  return raw
    .replace(/^\s*\/\*\*/, '')
    .replace(/\*\/\s*$/, '')
    .split('\n')
    .map(l => l.replace(/^\s*\*\s?/, ''))
    .join('\n')
    .trim();
}

let i = 0;

// collectDocIfPresent consumes a leading "/** ... */" block starting at
// the current line, if there is one, returning its cleaned text (or ''
// and no advance if the current line isn't a doc comment).
function collectDocIfPresent() {
  if (i < lines.length && /^\s*\/\*\*/.test(lines[i])) {
    const block = [];
    while (i < lines.length) {
      block.push(lines[i]);
      const closed = /\*\//.test(lines[i]);
      i++;
      if (closed) break;
    }
    return stripDocComment(block.join('\n'));
  }
  return '';
}

// collectStatement reads from the current line forward until every
// paren/brace/bracket opened within it is closed again AND the line
// ends with ';' or '}' — handles both single-line and multi-line
// (wrapped signatures, object types, function/class bodies) declarations
// uniformly. Checking both terminators at top level (rather than picking
// one based on whether a brace was ever seen) matters for real JS: an
// arrow-function const export like `export const f = (a, b) => {
// ... };` opens and closes a brace AND ends with ';' on its last line —
// requiring "sawBrace implies must end with '}'" would never terminate
// on that shape, since .d.ts declarations (where that assumption always
// held) never have a body at all.
function collectStatement() {
  let depthParen = 0, depthBrace = 0, depthBracket = 0;
  const buf = [];
  while (i < lines.length) {
    const line = lines[i];
    buf.push(line);
    for (const ch of line) {
      if (ch === '(') depthParen++;
      else if (ch === ')') depthParen--;
      else if (ch === '{') depthBrace++;
      else if (ch === '}') depthBrace--;
      else if (ch === '[') depthBracket++;
      else if (ch === ']') depthBracket--;
    }
    i++;
    const atTop = depthParen <= 0 && depthBrace <= 0 && depthBracket <= 0;
    if (!atTop) continue;
    const trimmed = line.trim();
    if (trimmed.endsWith('}') || trimmed.endsWith(';')) break;
  }
  return buf.join('\n');
}

// truncateBody renders just a declaration's own head (through its
// opening brace) for a kind whose body is executable implementation,
// not part of its public shape — a function/CommonJS-assignment body is
// irrelevant to the API surface, unlike an interface/type's object-type
// body, which IS the shape and is deliberately left untouched (see
// classify's own interface/type branches, which never call this).
function truncateBody(fullText) {
  const idx = fullText.indexOf('{');
  return idx === -1 ? fullText.trim() : fullText.slice(0, idx).trim() + ' { ... }';
}

// commonJSTarget matches the assignment-target prefix of a CommonJS
// export (`exports.foo = ...` or `module.exports.foo = ...`) — a single
// named export, not the whole-object-literal form (`module.exports =
// {...}`), which is out of scope (see this file's own header comment).
const commonJSTarget = '(?:module\\.)?exports\\.([A-Za-z0-9_$]+)\\s*=\\s*';

function classify(stmt) {
  const s = stmt.trim();
  let m;
  if ((m = /^export\s+(?:declare\s+)?function\s+([A-Za-z0-9_$]+)/.exec(s))) {
    return { kind: 'function', name: m[1], signature: truncateBody(s) };
  }
  if ((m = /^export\s+(?:declare\s+)?(?:abstract\s+)?class\s+([A-Za-z0-9_$]+)/.exec(s))) {
    return { kind: 'class', name: m[1], body: s };
  }
  if ((m = /^export\s+interface\s+([A-Za-z0-9_$]+)/.exec(s))) {
    return { kind: 'interface', name: m[1], signature: s };
  }
  if ((m = /^export\s+type\s+([A-Za-z0-9_$]+)/.exec(s))) {
    return { kind: 'type', name: m[1], signature: s };
  }
  if ((m = /^export\s+(?:declare\s+)?(?:const|let|var)\s+([A-Za-z0-9_$]+)/.exec(s))) {
    return { kind: 'const', name: m[1], signature: truncateBody(s) };
  }
  // CommonJS forms — plain-.js packages with no .d.ts at all (NORM-008's
  // JSDoc-fallback scope). TypeScript declaration files never contain
  // these, so they're additive, never in tension with the ES-module
  // branches above.
  if ((m = new RegExp('^' + commonJSTarget + 'function\\b').exec(s))) {
    return { kind: 'function', name: m[1], signature: truncateBody(s) };
  }
  if ((m = new RegExp('^' + commonJSTarget + 'class\\b').exec(s))) {
    return { kind: 'class', name: m[1], body: s };
  }
  if ((m = new RegExp('^' + commonJSTarget + '(?:async\\s+)?(?:\\([^)]*\\)|[A-Za-z0-9_$]+)\\s*=>').exec(s))) {
    return { kind: 'function', name: m[1], signature: truncateBody(s) };
  }
  return null;
}

// classMembers scans a class declaration's body for its own exported
// (i.e. not private/protected/#-prefixed) members.
function classMembers(fullClassText, className) {
  const open = fullClassText.indexOf('{');
  if (open === -1) return [];
  const body = fullClassText.slice(open + 1, fullClassText.lastIndexOf('}'));
  const memberLines = body.split(/\r\n|\n/);

  const members = [];
  let j = 0, pendingDoc = '';
  while (j < memberLines.length) {
    const raw = memberLines[j];
    if (/^\s*\/\*\*/.test(raw)) {
      const block = [];
      while (j < memberLines.length) {
        block.push(memberLines[j]);
        const closed = /\*\//.test(memberLines[j]);
        j++;
        if (closed) break;
      }
      pendingDoc = stripDocComment(block.join('\n'));
      continue;
    }
    const trimmed = raw.trim();
    if (trimmed === '') { j++; continue; }
    if (/^(private|protected|#)/.test(trimmed)) { pendingDoc = ''; j++; continue; }

    const m = /^(?:static\s+)?(?:public\s+)?(?:readonly\s+)?([A-Za-z0-9_$]+)\s*[:(<?]/.exec(trimmed);
    if (m) {
      let depthParen = 0, depthBrace = 0;
      const buf = [];
      let k = j;
      while (k < memberLines.length) {
        const l = memberLines[k];
        buf.push(l);
        for (const ch of l) {
          if (ch === '(') depthParen++;
          else if (ch === ')') depthParen--;
          else if (ch === '{') depthBrace++;
          else if (ch === '}') depthBrace--;
        }
        k++;
        const atTop = depthParen <= 0 && depthBrace <= 0;
        const t = l.trim();
        if (atTop && (t.endsWith('}') || t.endsWith(';'))) break;
      }
      members.push({ kind: 'method', name: m[1], receiver: className, signature: buf.join('\n').trim(), doc: pendingDoc });
      pendingDoc = '';
      j = k;
      continue;
    }
    pendingDoc = '';
    j++;
  }
  return members;
}

// isDeclarationLine matches the start of anything classify() can turn
// into a symbol — an ES `export ...` line, or a CommonJS `exports.foo =`
// / `module.exports.foo =` assignment line. Used to decide whether the
// current line is worth collectStatement-ing at all (and, in the
// module-doc peek below, whether a leading comment belongs to the first
// symbol instead of the module).
const declarationStart = /^\s*(export\s|(?:module\.)?exports\.[A-Za-z0-9_$]+\s*=)/;

// Step 1: a freestanding leading doc comment is the module's own doc —
// "freestanding" meaning it is NOT immediately (module blank lines
// aside) followed by an export statement, in which case it belongs to
// that symbol instead, not the module. Peeking ahead and rewinding if
// it turns out to be attached to an export, rather than unconditionally
// consuming it, is what keeps a doc comment sitting directly above the
// very first export (no separate module doc at all — the common case)
// correctly attached to that symbol instead of being silently swallowed
// as an empty-looking "module doc".
let fileDoc = '';
while (i < lines.length && /^\s*$/.test(lines[i])) i++;
if (i < lines.length && /^\s*\/\*\*/.test(lines[i])) {
  const savedI = i;
  const doc = collectDocIfPresent();
  let peek = i;
  while (peek < lines.length && /^\s*$/.test(lines[peek])) peek++;
  if (peek < lines.length && declarationStart.test(lines[peek])) {
    i = savedI; // belongs to that export, not the module — rewind
  } else {
    fileDoc = doc;
  }
}

// Step 2: scan the rest for exported declarations.
const symbols = [];
while (i < lines.length) {
  if (/^\s*$/.test(lines[i])) { i++; continue; }
  const doc = collectDocIfPresent();
  if (i >= lines.length) break;
  if (!declarationStart.test(lines[i])) { i++; continue; }

  const stmt = collectStatement();
  const cls = classify(stmt);
  if (!cls) continue;

  if (cls.kind === 'class') {
    symbols.push({ kind: 'class', name: cls.name, signature: truncateBody(cls.body), doc });
    for (const mem of classMembers(cls.body, cls.name)) {
      symbols.push(mem);
    }
  } else {
    symbols.push({ kind: cls.kind, name: cls.name, signature: cls.signature, doc });
  }
}

process.stdout.write(JSON.stringify({ doc: fileDoc, symbols }));
