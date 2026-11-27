import assert from 'node:assert/strict';
import { readFile, readdir } from 'node:fs/promises';
import ts from 'typescript';

for (const name of await readdir(new URL('./src/', import.meta.url))) {
  if (!name.endsWith('.ts')) continue;
  const text = await readFile(new URL('./src/' + name, import.meta.url), 'utf8');
  const root = ts.createSourceFile(name, text, ts.ScriptTarget.ESNext, true);
  const check = (node) => {
    if (ts.isPropertyAccessExpression(node)) {
      assert(!['innerHTML', 'outerHTML', 'insertAdjacentHTML'].includes(node.name.text),
        name + ' must construct DOM nodes and use textContent for API strings');
    }
    if (ts.isImportDeclaration(node)) {
      assert(node.moduleSpecifier.text.startsWith('./'),
        name + ' must use local relative module specifiers');
    }
    ts.forEachChild(node, check);
  };
  check(root);
}
