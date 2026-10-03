import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';

const componentsDir = import.meta.dirname;
const srcRoot = join(componentsDir, '..', '..', '..');

function read(relativePath) {
  return readFileSync(join(srcRoot, relativePath), 'utf8');
}

// Extract a top-level function's full source (brace-balanced scan).
function extractFunction(source, name) {
  const start = source.indexOf(`function ${name}(`);
  assert.notEqual(start, -1, `function ${name} not found`);
  const bodyStart = source.indexOf('{', start);
  let depth = 0;
  for (let i = bodyStart; i < source.length; i++) {
    if (source[i] === '{') depth++;
    else if (source[i] === '}') {
      depth--;
      if (depth === 0) return source.slice(start, i + 1);
    }
  }
  return source.slice(start);
}

function toPlainJs(fnSource) {
  const stripped = fnSource
    .replace('(resourceType: string, cleanupDays: number): number', '(resourceType, cleanupDays)')
    .replace('const defaults: Record<string, number> =', 'const defaults =');
  assert.notEqual(stripped, fnSource, 'type annotations must be stripped for plain-JS evaluation');
  return stripped;
}

const source = read('features/system/components/storage-policy-settings.tsx');
const cleanupDaysOnEnable = new Function(
  `return ${toPlainJs(extractFunction(source, 'cleanupDaysOnEnable'))}`
)();

test('enabling keeps positive days untouched', () => {
  assert.equal(cleanupDaysOnEnable('usage_logs', 30), 30);
  assert.equal(cleanupDaysOnEnable('requests', 1), 1);
});

test('enabling a legacy zero-day option fills the backend default days', () => {
  assert.equal(cleanupDaysOnEnable('requests', 0), 3);
  assert.equal(cleanupDaysOnEnable('usage_logs', 0), 30);
  assert.equal(cleanupDaysOnEnable('request_bodies', 0), 7);
  assert.equal(cleanupDaysOnEnable('response_bodies', 0), 7);
  assert.equal(cleanupDaysOnEnable('response_chunks', 0), 3);
});

test('negative days and unknown resources fall back to 30', () => {
  assert.equal(cleanupDaysOnEnable('usage_logs', -5), 30);
  assert.equal(cleanupDaysOnEnable('custom_resource', 0), 30);
});

test('the enable toggle wires the day auto-fill into the option patch', () => {
  assert.match(source, /if \(field === 'enabled' && value === true\) \{/);
  assert.match(source, /patch\.cleanupDays = cleanupDaysOnEnable\(resourceType, current\.cleanupDays\);/);
});
