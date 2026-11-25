import assert from 'node:assert/strict';
import test from 'node:test';
import { loadChannelSchema } from './load-channel-schema.mjs';

const { createChannelInputSchema, updateChannelInputSchema } = await loadChannelSchema();

function vertexRow(overrides = {}) {
  return { projectID: 'miguo-vertex-prod-1', apiKey: 'AQ.key-1', region: 'global', ...overrides };
}

const baseChannel = {
  name: 'Test Channel',
  baseURL: 'https://aiplatform.googleapis.com/v1',
  supportedModels: ['gemini-3.8-flash'],
  defaultTestModel: 'gemini-3.8-flash',
};

function issuePaths(result) {
  assert.equal(result.success, false, 'expected validation to fail');
  return result.error.issues.map((issue) => `${issue.path.join('.')}::${issue.message}`);
}

test('a Vertex channel rejects a row with a missing project ID', () => {
  const result = createChannelInputSchema.safeParse({
    ...baseChannel,
    type: 'gemini_vertex',
    credentials: { vertexApiKeys: [vertexRow({ projectID: '' })] },
  });

  const paths = issuePaths(result);
  assert.ok(
    paths.some((entry) => entry.startsWith('credentials.vertexApiKeys.0.projectID::')),
    `expected a projectID error at the row path, got ${JSON.stringify(paths)}`
  );
});

test('a Vertex channel rejects a row with a missing API key', () => {
  const result = createChannelInputSchema.safeParse({
    ...baseChannel,
    type: 'gemini_vertex',
    credentials: { vertexApiKeys: [vertexRow({ apiKey: '' })] },
  });

  const paths = issuePaths(result);
  assert.ok(
    paths.some((entry) => entry.startsWith('credentials.vertexApiKeys.0.apiKey::')),
    `expected an apiKey error at the row path, got ${JSON.stringify(paths)}`
  );
});

test('a Vertex channel rejects a project ID containing a path separator', () => {
  const result = createChannelInputSchema.safeParse({
    ...baseChannel,
    type: 'gemini_vertex',
    credentials: { vertexApiKeys: [vertexRow({ projectID: 'projects/evil' })] },
  });

  const paths = issuePaths(result);
  assert.ok(
    paths.some((entry) => entry.startsWith('credentials.vertexApiKeys.0.projectID::')),
    `expected a projectID error at the row path, got ${JSON.stringify(paths)}`
  );
});

test('a Vertex channel accepts a complete binding', () => {
  const result = createChannelInputSchema.safeParse({
    ...baseChannel,
    type: 'gemini_vertex',
    credentials: { vertexApiKeys: [vertexRow({ region: 'us-central1' })] },
  });

  assert.equal(result.success, true, JSON.stringify(result.error?.issues));
});

// An ordinary channel never submits vertexApiKeys, because onSubmit strips the field.
// A leftover row from toggling "Use Vertex AI" on and back off must not block it: the
// row is invisible in that state, so a save would fail with no field to correct.
test('an ordinary channel saves despite a leftover empty Vertex row', () => {
  const result = createChannelInputSchema.safeParse({
    ...baseChannel,
    type: 'openai',
    baseURL: 'https://api.openai.com/v1',
    credentials: {
      apiKeys: ['sk-test'],
      vertexApiKeys: [{ projectID: '', apiKey: '', region: 'global' }],
    },
  });

  assert.equal(result.success, true, JSON.stringify(result.error?.issues));
});

// The same rule at the write boundary: a Vertex key must not stand in for the API key of
// a channel that is not a Vertex channel. This fork makes regular channel API keys
// optional (see channel-config.test.mjs), so the schema accepts the payload; the Vertex
// rows are stripped on submit and rejected by the backend for non-Vertex channels, and
// the Vertex row rules must not fire for an ordinary channel.
test('an ordinary channel does not apply Vertex row rules (API keys optional in this fork)', () => {
  const result = createChannelInputSchema.safeParse({
    ...baseChannel,
    type: 'openai',
    baseURL: 'https://api.openai.com/v1',
    credentials: { vertexApiKeys: [vertexRow()] },
  });

  assert.equal(result.success, true, JSON.stringify(result.error?.issues));
});

test('an update rejects an emptied Vertex binding list', () => {
  const result = updateChannelInputSchema.safeParse({
    id: 'gid://axonhub/Channel/1',
    type: 'gemini_vertex',
    credentials: { vertexApiKeys: [] },
  });

  const paths = issuePaths(result);
  assert.ok(
    paths.some((entry) => entry === 'credentials.vertexApiKeys::At least one Vertex API key or service account JSON is required'),
    `expected the empty-list error, got ${JSON.stringify(paths)}`
  );
});

test('an update accepts a credential-only payload without Vertex rows', () => {
  const result = updateChannelInputSchema.safeParse({
    id: 'gid://axonhub/Channel/1',
    credentials: { apiKeys: ['sk-test'] },
  });

  assert.equal(result.success, true, JSON.stringify(result.error?.issues));
});
