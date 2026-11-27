#!/usr/bin/env node
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { fileURLToPath } from 'node:url';
import ts from 'typescript';
import { readRegistry } from './registry.mjs';

const normalize = s => s.replace(/^@openclaw\/plugin-sdk(?=\/|$)/, 'openclaw/plugin-sdk').replace(/\.js$/, '').replace(/^OpenClawPluginApi\./, 'api.').replace(/^PluginRuntime\./, 'api.runtime.');
const sdk = s => /^@?openclaw(?:\/plugin-sdk(?:\/|$)|$)/.test(s);

export function inventory(source, filename = 'index.js') {
  const sf = ts.createSourceFile(filename, source, ts.ScriptTarget.Latest, true);
  if (sf.parseDiagnostics.length) throw Error(`Cannot parse ${filename}`);
  const uses = [], gaps = [], bindings = new Map([['api', 'api']]);
  const add = (kind, surface, n) => {
    let branch;
    for (let p = n.parent; p; p = p.parent) if (ts.isConditionalExpression(p)) {
      branch = p.condition.getText(sf); break;
    }
    uses.push({ kind, surface: normalize(surface), file: filename, line: sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1, ...(branch ? { enclosingCondition: branch } : {}) });
  };
  function chain(n) {
    if (!n) return null;
    if (ts.isIdentifier(n)) return bindings.get(n.text) ?? null;
    if (ts.isAwaitExpression(n) || ts.isParenthesizedExpression(n)) return chain(n.expression);
    if (ts.isCallExpression(n) && n.expression.kind === ts.SyntaxKind.ImportKeyword && ts.isStringLiteral(n.arguments[0]) && sdk(n.arguments[0].text)) return normalize(n.arguments[0].text);
    if (ts.isPropertyAccessExpression(n)) { const base = chain(n.expression); return base ? base + '.' + n.name.text : null; }
    if (ts.isElementAccessExpression(n) && ts.isStringLiteral(n.argumentExpression)) { const base = chain(n.expression); return base ? base + '.' + n.argumentExpression.text : null; }
    return null;
  }
  const declarations = [];
  function imports(n) {
    if (ts.isVariableDeclaration(n)) declarations.push(n);
    if (ts.isImportDeclaration(n) || ts.isExportDeclaration(n)) {
      const spec = n.moduleSpecifier?.text;
      if (spec && sdk(spec)) {
        add('import', spec, n);
        const named = n.importClause?.namedBindings;
        if (named && ts.isNamedImports(named)) for (const el of named.elements) {
          const surface = normalize(spec) + '.' + (el.propertyName?.text ?? el.name.text);
          bindings.set(el.name.text, surface); add('symbol', surface, el);
        }
        if (named && ts.isNamespaceImport(named)) bindings.set(named.name.text, normalize(spec));
      }
    }
    if (ts.isCallExpression(n) && (n.expression.kind === ts.SyntaxKind.ImportKeyword || (ts.isIdentifier(n.expression) && n.expression.text === 'require'))) {
      const arg = n.arguments[0];
      if (arg && ts.isStringLiteral(arg) && sdk(arg.text)) add('import', arg.text, n);
      else if (!arg || !ts.isStringLiteral(arg)) gaps.push({ file: filename, line: sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1, reason: 'Computed module import; check manually.' });
    }
    ts.forEachChild(n, imports);
  }
  imports(sf);
  // Resolve simple const aliases and destructuring to their injected API origin.
  for (let round = 0; round <= declarations.length; round++) {
    let changed = false;
    for (const d of declarations) {
      let base = chain(d.initializer);
      const init = d.initializer && ts.isAwaitExpression(d.initializer) ? d.initializer.expression : d.initializer;
      if (init && ts.isCallExpression(init) && init.expression.kind === ts.SyntaxKind.ImportKeyword && ts.isStringLiteral(init.arguments[0]) && sdk(init.arguments[0].text)) base = normalize(init.arguments[0].text);
      if (!base) continue;
      const pairs = ts.isIdentifier(d.name) ? [[d.name.text, base]] : ts.isObjectBindingPattern(d.name) ? d.name.elements.filter(e => !e.dotDotDotToken && ts.isIdentifier(e.name)).map(e => [e.name.text, base + '.' + (e.propertyName?.text ?? e.name.text)]) : [];
      for (const [key, v] of pairs) if (bindings.get(key) !== v) { bindings.set(key, v); changed = true; }
    }
    if (!changed) break;
  }
  for (const d of declarations) if (ts.isObjectBindingPattern(d.name)) {
    const base = chain(d.initializer);
    if (base) for (const e of d.name.elements) if (!e.dotDotDotToken && ts.isIdentifier(e.name)) add('binding', base + '.' + (e.propertyName?.text ?? e.name.text), e);
  }
  function reference(n) {
    const p = n.parent;
    if (p.name === n && !ts.isShorthandPropertyAssignment(p)) return false;
    if (ts.isBindingElement(p) && (p.name === n || p.propertyName === n)) return false;
    if (ts.isImportSpecifier(p) || ts.isImportClause(p) || ts.isNamespaceImport(p) || ts.isExportSpecifier(p)) return false;
    if ((ts.isLabeledStatement(p) || ts.isBreakStatement(p) || ts.isContinueStatement(p)) && p.label === n) return false;
    return true;
  }
  function visit(n) {
    if (ts.isPropertyAccessExpression(n) || ts.isElementAccessExpression(n)) {
      const surface = chain(n);
      // Retain every prefix: a deprecated namespace can contain a current member.
      if (surface) add('member', surface, n);
      if (ts.isElementAccessExpression(n) && !ts.isStringLiteral(n.argumentExpression) && chain(n.expression)) gaps.push({ file: filename, line: sf.getLineAndCharacterOfPosition(n.getStart(sf)).line + 1, reason: 'Computed API member; check manually.' });
    }
    if (ts.isIdentifier(n) && (sdk(bindings.get(n.text) ?? '') || (bindings.get(n.text) ?? '').startsWith('api.')) && reference(n)) add('symbol', bindings.get(n.text), n);
    if (ts.isCallExpression(n) && n.arguments[0] && ((ts.isPropertyAccessExpression(n.expression) && ['request', 'call', 'dispatch'].includes(n.expression.name.text)) || (ts.isIdentifier(n.expression) && n.expression.text === 'dispatch')) && ts.isStringLiteral(n.arguments[0])) add('rpc', n.arguments[0].text, n);
    ts.forEachChild(n, visit);
  }
  visit(sf);
  return { uses: [...new Map(uses.map(u => [JSON.stringify(u), u])).values()], gaps };
}

function* files(dir, prefix = '') {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    if (e.name.startsWith('.') || ['node_modules', 'test', 'tests', 'testdata', 'coverage'].includes(e.name)) continue;
    const rel = prefix + e.name;
    if (e.isDirectory()) yield* files(path.join(dir, e.name), rel + '/');
    else if (e.isFile() && /\.[cm]?[jt]sx?$/.test(e.name) && !/\.(?:test|spec|d)\.[cm]?[jt]sx?$/.test(e.name)) yield rel;
  }
}

export function assess(host, plugins, date = new Date().toISOString().slice(0, 10)) {
  const pkgText = fs.readFileSync(path.join(host, 'package.json'), 'utf8'), pkg = JSON.parse(pkgText);
  const records = readRegistry(host), findings = [], usages = [], gaps = [], inputs = [];
  for (const plugin of plugins) {
    const root = fs.realpathSync(plugin), manifest = JSON.parse(fs.readFileSync(path.join(root, 'openclaw.plugin.json'), 'utf8'));
    const name = manifest.id;
    for (const file of files(root)) {
      const source = fs.readFileSync(path.join(root, file), 'utf8');
      inputs.push({ plugin: name, file, sha256: crypto.createHash('sha256').update(source).digest('hex') });
      const inv = inventory(source, file);
      usages.push(...inv.uses.map(u => ({ plugin: name, ...u })));
      gaps.push(...inv.gaps.map(g => ({ plugin: name, ...g })));
    }
  }
  for (const u of usages) {
    if (u.kind === 'import') {
      const key = u.surface === 'openclaw' ? '.' : './' + u.surface.replace(/^openclaw\//, '');
      const exported = Object.keys(pkg.exports ?? {}).some(pattern => pattern === key || (pattern.includes('*') && key.startsWith(pattern.split('*')[0]) && key.endsWith(pattern.split('*')[1])));
      if (!exported) findings.push({ severity: 'error', code: 'missing-sdk-subpath', ...u, replacement: 'Select a subpath exported by this host; confirm the built package too.' });
    }
    for (const r of records) if (r.status !== 'active' && r.surfaces.some(s => normalize(s) === u.surface)) {
      findings.push({ ...u, severity: r.status === 'removed' ? 'error' : 'warning', code: r.code, status: r.status, removeAfter: r.removeAfter ?? null, reviewDateReached: r.removeAfter ? date >= r.removeAfter : false, removalGate: r.removalGate ?? null, replacement: r.replacement, docs: r.docsPath ? 'https://docs.openclaw.ai' + r.docsPath : 'https://docs.openclaw.ai/plugins/compatibility' });
    }
  }
  return { schema: 1, hostVersion: pkg.version, assessedOn: date, registryRecords: records.length, sourceFingerprint: crypto.createHash('sha256').update(JSON.stringify({ pkg: pkgText, records })).digest('hex'), inputs, findings, gaps, inventory: usages, limits: ['Static candidates, not runtime reachability or a support certification.', 'Simple aliases are resolved file-wide; shadowing, reassignment, cross-file wrappers and generated dispatch need manual review.', 'Registry surfaces with prose or signatures are not inferred from leaf-name matches.', 'Exact SDK subpaths are checked; named exports, RPC schemas, host configuration and permissions require native validation and integration tests.', 'The host Doctor migration registry is separate and is not assessed. Elapsed review dates never imply an API has been removed.'] };
}

export function format(report) {
  const lines = [`OpenClaw ${report.hostVersion}: ${report.inputs.length} files, ${report.registryRecords} compatibility records`, ''];
  for (const f of report.findings) lines.push(`${f.severity.toUpperCase()} ${f.plugin}/${f.file}:${f.line} ${f.surface}`, `  ${f.code}${f.status ? ' (' + f.status + ')' : ''}${f.removeAfter ? '; review date ' + f.removeAfter : ''}`, ...(f.enclosingCondition ? ['  Enclosing condition (reachability not inferred): ' + f.enclosingCondition] : []), `  ${f.replacement}`, ...(f.docs ? ['  ' + f.docs] : []));
  lines.push('', `${report.findings.filter(f => f.severity === 'error').length} errors; ${report.findings.filter(f => f.severity === 'warning').length} static warnings; ${report.gaps.length} computed-access gaps.`, ...report.limits.map(l => '- ' + l));
  return lines.join('\n');
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const args = process.argv.slice(2), plugins = []; let host, json = false, failOnWarning = false;
    for (let i = 0; i < args.length; i++) {
      if (args[i] === '--host') host = args[++i];
      else if (args[i] === '--plugin') plugins.push(args[++i]);
      else if (args[i] === '--json') json = true;
      else if (args[i] === '--fail-on-warning') failOnWarning = true;
      else throw Error(`Unknown argument: ${args[i]}`);
    }
    if (!host || !plugins.length || plugins.some(p => !p)) throw Error('Usage: node audit.mjs --host <OpenClaw source checkout> --plugin <plugin package> [--plugin ...] [--json]');
    const report = assess(host, plugins);
    console.log(json ? JSON.stringify(report, null, 2) : format(report));
    process.exitCode = report.findings.some(f => f.severity === 'error' || failOnWarning) ? 1 : 0;
  } catch (e) {
    console.error(`Audit incomplete: ${e.message}`);
    process.exitCode = 2;
  }
}
