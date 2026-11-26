// Read OpenClaw's data declarations; never import or evaluate its JavaScript.
import fs from 'node:fs';
import path from 'node:path';
import ts from 'typescript';

export function readRegistry(host) {
  const root = fs.realpathSync(path.join(host, 'src/plugins/compat'));
  const modules = new Map();
  function load(file) {
    file = fs.realpathSync(file);
    if (!file.startsWith(root + path.sep)) throw Error('Registry import escapes its directory');
    if (modules.has(file)) return modules.get(file);
    const source = ts.createSourceFile(file, fs.readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true);
    if (source.parseDiagnostics.length) throw Error(`Cannot parse registry: ${path.basename(file)}`);
    const env = new Map();
    modules.set(file, env);
    for (const node of source.statements) {
      if (ts.isImportDeclaration(node) && !node.importClause?.isTypeOnly) {
        const spec = node.moduleSpecifier.text;
        if (!spec.startsWith('./')) throw Error('Registry needs an unsupported external import');
        const other = load(path.resolve(path.dirname(file), spec.replace(/\.js$/, '.ts')));
        for (const item of node.importClause?.namedBindings?.elements ?? []) {
          if (!item.isTypeOnly) env.set(item.name.text, () => resolve(other, item.propertyName?.text ?? item.name.text));
        }
      }
      if (ts.isFunctionDeclaration(node) && node.name) env.set(node.name.text, { node, env });
      if (ts.isVariableStatement(node)) for (const item of node.declarationList.declarations) {
        if (!ts.isIdentifier(item.name)) throw Error('Unsupported registry declaration');
        env.set(item.name.text, () => value(item.initializer, env));
      }
    }
    return env;
  }
  function resolve(env, name) {
    if (name === 'undefined') return undefined;
    if (!env.has(name)) throw Error(`Unresolved registry name: ${name}`);
    const v = env.get(name);
    if (typeof v !== 'function') return v;
    const result = v(); env.set(name, result); return result;
  }
  function bind(pattern, input, env) {
    if (ts.isIdentifier(pattern)) { env.set(pattern.text, input); return; }
    if (!ts.isObjectBindingPattern(pattern)) throw Error('Unsupported registry parameter');
    const used = new Set();
    for (const item of pattern.elements) {
      if (item.dotDotDotToken) { bind(item.name, Object.fromEntries(Object.entries(input).filter(([k]) => !used.has(k))), env); continue; }
      const key = item.propertyName?.text ?? item.name.text;
      used.add(key); bind(item.name, input[key], env);
    }
  }
  function call(fn, args) {
    if (!fn?.node) throw Error('Unsupported registry function');
    const env = new Map(fn.env);
    fn.node.parameters.forEach((p, i) => bind(p.name, args[i], env));
    const body = fn.node.body;
    return ts.isBlock(body) ? statements(body.statements, env)?.result : value(body, env);
  }
  function statements(nodes, env) {
    for (const n of nodes) {
      if (ts.isReturnStatement(n)) return { result: value(n.expression, env) };
      if (ts.isBlock(n)) { const r = statements(n.statements, new Map(env)); if (r) return r; }
      else if (ts.isVariableStatement(n)) for (const d of n.declarationList.declarations) bind(d.name, value(d.initializer, env), env);
      else if (ts.isIfStatement(n)) {
        const branch = value(n.expression, env) ? n.thenStatement : n.elseStatement;
        if (branch) { const r = statements([branch], env); if (r) return r; }
      } else throw Error(`Unsupported registry statement: ${ts.SyntaxKind[n.kind]}`);
    }
  }
  function value(n, env) {
    if (!n) return undefined;
    if (ts.isAsExpression(n) || ts.isSatisfiesExpression(n) || ts.isParenthesizedExpression(n)) return value(n.expression, env);
    if (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n)) return n.text;
    if (ts.isNumericLiteral(n)) return Number(n.text);
    if (n.kind === ts.SyntaxKind.TrueKeyword) return true;
    if (n.kind === ts.SyntaxKind.FalseKeyword) return false;
    if (n.kind === ts.SyntaxKind.NullKeyword) return null;
    if (ts.isIdentifier(n)) return resolve(env, n.text);
    if (ts.isArrowFunction(n)) return { node: n, env };
    if (ts.isArrayLiteralExpression(n)) return n.elements.flatMap(e => ts.isSpreadElement(e) ? value(e.expression, env) : [value(e, env)]);
    if (ts.isObjectLiteralExpression(n)) {
      const out = Object.create(null);
      for (const p of n.properties) {
        if (ts.isSpreadAssignment(p)) Object.assign(out, value(p.expression, env));
        else if (ts.isShorthandPropertyAssignment(p)) out[p.name.text] = resolve(env, p.name.text);
        else if (ts.isPropertyAssignment(p) && !ts.isComputedPropertyName(p.name)) out[p.name.text] = value(p.initializer, env);
        else throw Error('Unsupported registry object member');
      }
      return out;
    }
    if (ts.isPropertyAccessExpression(n)) return value(n.expression, env)[n.name.text];
    if (ts.isTemplateExpression(n)) return n.head.text + n.templateSpans.map(s => String(value(s.expression, env)) + s.literal.text).join('');
    if (ts.isConditionalExpression(n)) return value(value(n.condition, env) ? n.whenTrue : n.whenFalse, env);
    if (ts.isBinaryExpression(n)) {
      const left = value(n.left, env);
      if (n.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken) return left && value(n.right, env);
      if (n.operatorToken.kind === ts.SyntaxKind.BarBarToken) return left || value(n.right, env);
      if (n.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken) return left ?? value(n.right, env);
      const right = value(n.right, env);
      if (n.operatorToken.kind === ts.SyntaxKind.EqualsEqualsEqualsToken) return left === right;
      if (n.operatorToken.kind === ts.SyntaxKind.InKeyword) return left in right;
    }
    if (ts.isCallExpression(n)) {
      if (ts.isPropertyAccessExpression(n.expression) && ['map', 'flatMap'].includes(n.expression.name.text)) {
        const array = value(n.expression.expression, env), fn = value(n.arguments[0], env);
        if (!Array.isArray(array)) throw Error('Registry map receiver is not data');
        return array[n.expression.name.text](x => call(fn, [x]));
      }
      return call(value(n.expression, env), n.arguments.map(a => value(a, env)));
    }
    throw Error(`Unsupported registry expression: ${ts.SyntaxKind[n.kind]}`);
  }
  const records = resolve(load(path.join(root, 'registry-records.ts')), 'PLUGIN_COMPAT_RECORDS');
  const codes = new Set();
  for (const r of records) {
    if (!r.code || !['active', 'deprecated', 'removal-pending', 'removed'].includes(r.status) || !Array.isArray(r.surfaces) || codes.has(r.code)) throw Error('Malformed or duplicate registry record');
    codes.add(r.code);
  }
  return records;
}
