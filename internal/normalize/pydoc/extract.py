"""extract.py: a dependency-free static extractor for one Python module.

Uses only the standard library's `ast` module — the target package's
code is never imported or executed. Mirrors tsdoc's extract.js in shape:
reads a module file, emits one JSON object on stdout describing the
module's own docstring plus its resolved public symbols (functions,
classes with their own public methods).

Usage: python3 extract.py <entry.py> [<package-dir>]

Export resolution (NORM-009's own scope, deliberately bounded):
  - An explicit, statically-literal `__all__ = [...]` is authoritative.
  - Otherwise, every non-underscore-prefixed top-level function/class,
    plus every name reachable via a *single-hop* relative import
    ("from .module import Name") resolved against a sibling file in the
    same package directory — not a multi-hop re-export chain.
  - `__all__` present but not a static list of string constants is
    recorded as a coverage limitation (exports_dynamic: true), not
    silently dropped or guessed at.
"""
import ast
import copy
import json
import os
import sys


def render_signature(node):
    """Renders node's own header (def/class line) via ast.unparse — the
    stdlib's own unparser, not a hand-rolled one, so defaults, type
    annotations, *args/**kwargs, and positional/keyword-only markers all
    render correctly without this script reimplementing Python grammar.
    """
    n = copy.deepcopy(node)
    n.decorator_list = []
    n.body = [ast.Expr(value=ast.Constant(value=Ellipsis))]
    return ast.unparse(n).split('\n')[0]


def is_public(name):
    return not name.startswith('_') or name == '__init__'


def class_members(node):
    members = []
    for item in node.body:
        if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)) and is_public(item.name):
            members.append({
                'kind': 'method',
                'name': item.name,
                'receiver': node.name,
                'signature': render_signature(item),
                'doc': ast.get_docstring(item) or '',
            })
    return members


def parse_module(path):
    with open(path, 'r', encoding='utf-8', errors='replace') as f:
        source = f.read()
    return ast.parse(source, filename=path)


def scan(tree):
    """Returns (defs, reexports, explicit_all, all_is_dynamic) — defs
    maps a top-level name to its own {kind,name,signature,doc,members},
    reexports maps a re-exported name to (module_name_or_None, original_name)
    for a relative "from . import x" / "from .mod import x" statement.
    """
    defs = {}
    reexports = {}
    explicit_all = None
    all_is_dynamic = False

    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and is_public(node.name):
            defs[node.name] = {
                'kind': 'function', 'name': node.name,
                'signature': render_signature(node),
                'doc': ast.get_docstring(node) or '',
            }
        elif isinstance(node, ast.ClassDef) and is_public(node.name):
            defs[node.name] = {
                'kind': 'class', 'name': node.name,
                'signature': render_signature(node),
                'doc': ast.get_docstring(node) or '',
                'members': class_members(node),
            }
        elif isinstance(node, ast.ImportFrom) and (node.level or 0) >= 1:
            for alias in node.names:
                exported_name = alias.asname or alias.name
                reexports[exported_name] = (node.module, alias.name)
        elif isinstance(node, ast.Assign):
            targets = [t.id for t in node.targets if isinstance(t, ast.Name)]
            if '__all__' in targets:
                if isinstance(node.value, (ast.List, ast.Tuple)) and all(
                    isinstance(e, ast.Constant) and isinstance(e.value, str) for e in node.value.elts
                ):
                    explicit_all = [e.value for e in node.value.elts]
                else:
                    all_is_dynamic = True

    return defs, reexports, explicit_all, all_is_dynamic


def resolve_reexport(pkg_dir, module_name, original_name):
    """Resolves a single-hop "from .module import original_name" (or
    "from . import original_name" when module_name is None) against a
    sibling file/package in pkg_dir. Returns a symbol dict or None if it
    can't be statically resolved — never a guess.
    """
    if module_name is None:
        candidates = [os.path.join(pkg_dir, original_name + '.py'),
                      os.path.join(pkg_dir, original_name, '__init__.py')]
        target_name = original_name
    else:
        rel = module_name.replace('.', os.sep)
        candidates = [os.path.join(pkg_dir, rel + '.py'),
                      os.path.join(pkg_dir, rel, '__init__.py')]
        target_name = original_name

    for path in candidates:
        if os.path.isfile(path):
            try:
                tree = parse_module(path)
            except SyntaxError:
                return None
            defs, _, _, _ = scan(tree)
            if target_name in defs:
                return defs[target_name]
    return None


def apply_pyi_overrides(pkg_dir, entry_path, defs):
    """A .pyi stub's signature is authoritative when both exist; a
    docstring is kept from the .py source when the stub's own is empty —
    never discarded just because a stub happened to exist.
    """
    stub_path = os.path.splitext(entry_path)[0] + '.pyi'
    if not os.path.isfile(stub_path):
        return defs
    try:
        stub_tree = parse_module(stub_path)
    except SyntaxError:
        return defs
    stub_defs, _, _, _ = scan(stub_tree)
    for name, stub_def in stub_defs.items():
        if name in defs:
            defs[name]['signature'] = stub_def['signature']
            if not defs[name].get('doc'):
                defs[name]['doc'] = stub_def.get('doc', '')
        else:
            defs[name] = stub_def
    return defs


def main():
    entry_path = sys.argv[1]
    pkg_dir = sys.argv[2] if len(sys.argv) > 2 else os.path.dirname(entry_path)

    tree = parse_module(entry_path)
    module_doc = ast.get_docstring(tree) or ''
    defs, reexports, explicit_all, all_is_dynamic = scan(tree)
    defs = apply_pyi_overrides(pkg_dir, entry_path, defs)

    if explicit_all is not None:
        export_names = explicit_all
    else:
        export_names = [n for n in defs if is_public(n)] + [n for n in reexports if is_public(n)]

    symbols = []
    seen = set()
    for name in export_names:
        if name in seen:
            continue
        seen.add(name)
        if name in defs:
            entry = defs[name]
        elif name in reexports:
            module_name, original_name = reexports[name]
            entry = resolve_reexport(pkg_dir, module_name, original_name)
            if entry is None:
                continue
        else:
            continue

        symbols.append({
            'kind': entry['kind'], 'name': name,
            'signature': entry.get('signature', ''), 'doc': entry.get('doc', ''),
        })
        for m in entry.get('members', []):
            symbols.append(m)

    print(json.dumps({'doc': module_doc, 'symbols': symbols, 'exportsDynamic': all_is_dynamic}))


if __name__ == '__main__':
    main()
