import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { inventory, assess } from './audit.mjs';
import { readRegistry } from './registry.mjs';
const cli = fileURLToPath(new URL('./audit.mjs', import.meta.url));

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'openclaw-audit-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const compat = path.join(root, 'host/src/plugins/compat'), plugin = path.join(root, 'plugin');
  fs.mkdirSync(compat, { recursive: true }); fs.mkdirSync(plugin);
  fs.writeFileSync(path.join(root, 'host/package.json'), JSON.stringify({ version: '2026.11.10', exports: { '.': './index.js', './plugin-sdk/current': './current.js' } }));
  fs.writeFileSync(path.join(plugin, 'openclaw.plugin.json'), '{"id":"sample"}');
  const records = [{ code: 'old', status: 'removal-pending', owner: 'sdk', removeAfter: '2026-10-01', surfaces: ['api.runtime.agent.session.old'], replacement: 'Await current()', docsPath: '/plugins/compatibility' }];
  fs.writeFileSync(path.join(compat, 'registry-records.ts'), `export const PLUGIN_COMPAT_RECORDS = ${JSON.stringify(records)} as const;`);
  return { root, compat, plugin, host: path.join(root, 'host') };
}

test('AST inventory follows aliases, destructuring and optional/literal members, not comments or strings', () => {
  const { uses } = inventory(`// api.runtime.agent.session.imaginary()
    const text = 'api.runtime.agent.session.imaginary';
    import { current as renamed } from 'openclaw/plugin-sdk/current';
    const sessions = api.runtime.agent.session;
    const { old: readOld } = sessions;
    sessions?.['old'](); renamed();
    client.request('sessions.list', {}); dispatch('sessions.github.publish', {});
  `);
  assert(uses.some(u => u.surface === 'api.runtime.agent.session.old'));
  assert(uses.some(u => u.surface === 'openclaw/plugin-sdk/current.current'));
  assert(uses.some(u => u.kind === 'rpc' && u.surface === 'sessions.list'));
  assert(uses.some(u => u.kind === 'rpc' && u.surface === 'sessions.github.publish'));
  assert(!uses.some(u => u.surface.includes('imaginary')));
  const inline = inventory(`const fn = async () => (await import('openclaw/plugin-sdk/current')).old();`);
  assert(inline.uses.some(u => u.surface === 'openclaw/plugin-sdk/current.old'));
});

test('dates do not turn removal-pending into removed; missing imports are errors', t => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.plugin, 'index.js'), `import 'openclaw/plugin-sdk/absent'; const s = api.runtime.agent.session; s.old();`);
  const r = assess(f.host, [f.plugin], '2026-11-26');
  assert.equal(r.findings.find(x => x.code === 'old').severity, 'warning');
  assert.equal(r.findings.find(x => x.code === 'old').reviewDateReached, true);
  assert.equal(r.findings.find(x => x.code === 'missing-sdk-subpath').severity, 'error');
});

test('member match never confuses a replacement suffix with an old method', t => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.plugin, 'index.js'), `api.runtime.agent.session.oldAsync();`);
  assert.equal(assess(f.host, [f.plugin]).findings.length, 0);
});

test('computed access is explicit and fallback context does not claim runtime use', () => {
  const r = inventory(`const s=api.runtime.agent.session;
    const read = x => typeof s.currentAsync === 'function' ? s.currentAsync(x) : s.old(x);
    api.session[key](); import(moduleName);`);
  assert.equal(r.gaps.length, 2);
  assert.match(r.uses.find(u => u.surface.endsWith('.old')).enclosingCondition, /currentAsync/);
});

test('registry reads imported data, spreads, mapped records and helper returns without executing code', t => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.compat, 'data.ts'), `const shared={status:'deprecated',owner:'sdk'} as const;
    const seeds=[{code:'mapped',name:'old'}];
    function build({name,...rest}) { return {...shared,...rest,surfaces:[\`api.\${name}\`]}; }
    export const DATA=seeds.map(build);`);
  fs.writeFileSync(path.join(f.compat, 'registry-records.ts'), `import {DATA} from './data.js';
    function wrap<T>(x:T){return x;} export const PLUGIN_COMPAT_RECORDS=wrap([...DATA]);`);
  assert.equal(readRegistry(f.host)[0].surfaces[0], 'api.old');
  fs.writeFileSync(path.join(f.compat, 'registry-records.ts'), `export const PLUGIN_COMPAT_RECORDS = process.exit(42);`);
  assert.throws(() => readRegistry(f.host), /Unresolved registry name/);
});

test('unknown registry syntax cannot produce a clean assessment', t => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.compat, 'registry-records.ts'), 'export const PLUGIN_COMPAT_RECORDS = JSON.parse("[]");');
  const r = spawnSync(process.execPath, [cli, '--host', f.host, '--plugin', f.plugin], { encoding: 'utf8' });
  assert.equal(r.status, 2); assert.match(r.stderr, /Audit incomplete/);
});

test('never executes plugin code; scans shipped dist but skips tests and symlinks', t => {
  const f = fixture(t), marker = path.join(f.root, 'executed');
  fs.writeFileSync(path.join(f.plugin, 'index.js'), `require('node:fs').writeFileSync(${JSON.stringify(marker)}, 'bad');`);
  fs.mkdirSync(path.join(f.plugin, 'dist'));
  fs.writeFileSync(path.join(f.plugin, 'dist/ui.js'), 'api.runtime.agent.session.old();');
  fs.writeFileSync(path.join(f.plugin, 'fixture.test.js'), `import 'openclaw/plugin-sdk/absent';`);
  fs.symlinkSync(f.host, path.join(f.plugin, 'external'));
  const r = assess(f.host, [f.plugin]);
  assert(!fs.existsSync(marker)); assert.equal(r.findings.length, 1); assert.equal(r.findings[0].file, 'dist/ui.js');
  assert.deepEqual(r.inputs.map(x => x.file).sort(), ['dist/ui.js', 'index.js']);
});

test('root SDK export and wildcard exports are not missing subpaths', t => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.host, 'package.json'), JSON.stringify({ version:'2026.11.10',exports:{'.':'./index.js','./plugin-sdk/*':'./dist/*.js'} }));
  fs.writeFileSync(path.join(f.plugin, 'index.js'), `import 'openclaw'; import 'openclaw/plugin-sdk/focused';`);
  assert.equal(assess(f.host, [f.plugin]).findings.length, 0);
});

test('CLI exit 0/1, strict warnings and removed members work from any cwd', t => {
  const f = fixture(t);
  const run = (...extra) => spawnSync(process.execPath, [cli, '--host', f.host, '--plugin', f.plugin, '--json', ...extra], { cwd: f.plugin, encoding: 'utf8' });
  fs.writeFileSync(path.join(f.plugin, 'index.js'), 'api.runtime.agent.session.current();');
  assert.equal(run().status, 0);
  fs.writeFileSync(path.join(f.plugin, 'index.js'), 'api.runtime.agent.session.old();');
  assert.equal(run().status, 0); assert.equal(run('--fail-on-warning').status, 1);
  const file = path.join(f.compat, 'registry-records.ts');
  fs.writeFileSync(file, fs.readFileSync(file, 'utf8').replace('removal-pending', 'removed'));
  const r = run(); assert.equal(r.status, 1);
  assert.equal(JSON.parse(r.stdout).findings[0].severity, 'error');
});

test('unrelated property names, labels and bindings are not alias references', () => {
  const source = `const { old: foo } = api.runtime.agent.session;
    other.foo; const obj = { foo: 1, foo() {} };
    const { foo: bar } = other;
    function container(foo) {};
    foo: for (;;) { break foo; }
    const actual = {foo};`;
  const r = inventory(source);
  const refs = r.uses.filter(u => u.kind === 'symbol' && u.surface === 'api.runtime.agent.session.old');
  assert.equal(refs.length, 1); assert.equal(refs[0].line, 6);
  assert(r.uses.some(u => u.kind === 'binding' && u.surface === 'api.runtime.agent.session.old'));
});

test('scoped SDK names, .js suffixes and type surface names normalize consistently', t => {
  const f = fixture(t);
  fs.writeFileSync(path.join(f.host, 'package.json'), JSON.stringify({version:'2026.11.10', exports:{'./plugin-sdk':'./root.js','./plugin-sdk/current':'./current.js'}}));
  const records = [
    {code:'api-alias',status:'deprecated',surfaces:['OpenClawPluginApi.old']},
    {code:'runtime-alias',status:'deprecated',surfaces:['PluginRuntime.agent.old']},
    {code:'sdk-symbol',status:'removed',surfaces:['openclaw/plugin-sdk/current.old']},
  ];
  fs.writeFileSync(path.join(f.compat,'registry-records.ts'), `export const PLUGIN_COMPAT_RECORDS=${JSON.stringify(records)};`);
  fs.writeFileSync(path.join(f.plugin,'index.js'), `import '@openclaw/plugin-sdk';
    import {old as unused} from '@openclaw/plugin-sdk/current.js';
    api.old(); api.runtime.agent.old();`);
  const r=assess(f.host,[f.plugin]);
  assert(!r.findings.some(x=>x.code==='missing-sdk-subpath'));
  for(const code of records.map(x=>x.code)) assert(r.findings.some(x=>x.code===code),code);
});

test('unsupported registry forms, malformed records and plugin syntax exit incomplete', t => {
  const f=fixture(t), registry=path.join(f.compat,'registry-records.ts');
  const run=()=>spawnSync(process.execPath,[cli,'--host',f.host,'--plugin',f.plugin],{encoding:'utf8'});
  const cases = [
    ['export const PLUGIN_COMPAT_RECORDS=new Array();', /Unsupported registry expression/],
    ['function f(){while(true){}} export const PLUGIN_COMPAT_RECORDS=f();', /Unsupported registry statement/],
    ['export const PLUGIN_COMPAT_RECORDS=[{get code(){return "x"}}];', /Unsupported registry object member/],
    ['export const PLUGIN_COMPAT_RECORDS=[{code:"x",surfaces:[]}];', /Malformed or duplicate/],
    ['const r={code:"x",status:"removed",surfaces:[]}; export const PLUGIN_COMPAT_RECORDS=[r,r];', /Malformed or duplicate/],
  ];
  for(const [source,message] of cases){
    fs.writeFileSync(registry,source);const r=run();assert.equal(r.status,2);assert.match(r.stderr,message);
  }
  fs.writeFileSync(registry,'export const PLUGIN_COMPAT_RECORDS=[];');
  fs.writeFileSync(path.join(f.plugin,'index.js'),'function {');
  const r=run();assert.equal(r.status,2);assert.match(r.stderr,/Cannot parse/);
});
