import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import { providersDataSchema } from './providers.schema.ts';

const providersJsonPath = join(import.meta.dirname, 'providers.json');
const backendProvidersJsonPath = join(
  import.meta.dirname,
  '../../../../../internal/server/biz/catalogdata/providers.json'
);

function formatIssue(issue) {
  const path = issue.path.length > 0 ? issue.path.join('.') : '<root>';
  return `  ${path}: ${issue.message}`;
}

test('bundled providers.json matches providersDataSchema', () => {
  const raw = JSON.parse(readFileSync(providersJsonPath, 'utf8'));
  const result = providersDataSchema.safeParse(raw);

  if (!result.success) {
    const issues = result.error.issues;
    const shown = issues.slice(0, 10).map(formatIssue).join('\n');
    const rest = issues.length > 10 ? `\n  ... and ${issues.length - 10} more` : '';
    assert.fail(
      `providers.json (synced by scripts/sync/sync-model-developers.js) no longer matches the schema that providers.ts parses at import time.\nUpdate providers.schema.ts to accept the new shape:\n${shown}${rest}`
    );
  }

  assert.ok(Object.keys(result.data.providers).length > 0, 'bundled providers.json must contain at least one provider');
});

// Both the frontend fallback catalog and the backend embedded catalog are copies of the
// same upstream snapshot. A model id must be unique inside one provider, otherwise the
// channel model pickers see the same model twice and price lookups become ambiguous.
// The same id in different providers is legitimate and must not be flagged.
test('bundled provider catalogs keep model ids unique within each provider', () => {
  const catalogs = [
    { label: 'frontend bundled providers.json', path: providersJsonPath },
    { label: 'backend catalogdata/providers.json', path: backendProvidersJsonPath },
  ];

  for (const catalog of catalogs) {
    const raw = JSON.parse(readFileSync(catalog.path, 'utf8'));

    for (const [providerId, provider] of Object.entries(raw.providers)) {
      const seen = new Set();
      const duplicates = new Set();

      for (const model of provider.models ?? []) {
        if (seen.has(model.id)) duplicates.add(model.id);
        seen.add(model.id);
      }

      assert.deepEqual(
        [...duplicates],
        [],
        `${catalog.label}: provider "${providerId}" contains duplicate model ids: ${[...duplicates].join(', ')}`
      );
    }
  }
});
