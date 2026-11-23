import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import ts from 'typescript';

// The repo has no React hook test setup, so the REAL onSuccess body of
// useFetchModels is extracted verbatim from channels.ts and executed against
// stubbed toast/i18n. This pins the Codex catalog fallback contract: Codex
// fallback is reported inline by channels-action-dialog.tsx (and the Codex
// skipped reason), so the hook must stay silent there, while non-Codex
// fallback (e.g. Cline) keeps the generic fallback warning.
const source = readFileSync(join(import.meta.dirname, 'channels.ts'), 'utf8');

// The AST is the parser of record: a brace-counting scanner would stop at a
// `}` inside a comment or string literal, silently truncating the handler.
const ast = ts.createSourceFile('channels.ts', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.TS);
const tsConfig = { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2023 };

function findUseFetchModelsConfig() {
  const hook = ast.statements.find((node) => ts.isFunctionDeclaration(node) && node.name?.text === 'useFetchModels');
  assert.ok(hook, 'useFetchModels must stay in channels.ts');

  // The hook returns useMutation({ ... }); walk statements, not raw text.
  const useMutationCall = hook.body.statements
    .filter((statement) => ts.isReturnStatement(statement) && statement.expression)
    .map((statement) => statement.expression)
    .find((expression) => ts.isCallExpression(expression) && expression.expression.getText(ast) === 'useMutation');
  assert.ok(useMutationCall, 'useFetchModels must return a useMutation call');

  const config = useMutationCall.arguments[0];
  assert.ok(config && ts.isObjectLiteralExpression(config), 'useMutation must receive an options object');
  return config;
}

function findProperty(objectLiteral, name) {
  return objectLiteral.properties.find(
    (property) => ts.isPropertyAssignment(property) && property.name.getText(ast) === name
  );
}

const mutationConfig = findUseFetchModelsConfig();
const onSuccessProperty = findProperty(mutationConfig, 'onSuccess');
assert.ok(onSuccessProperty, 'useFetchModels must keep its onSuccess handler');

const onSuccessSource = onSuccessProperty.initializer.getText(ast);
const transpiled = ts.transpileModule(`const __onSuccess = ${onSuccessSource};`, {
  compilerOptions: tsConfig,
}).outputText;

const handlerFactory = new Function('toast', 't', `${transpiled}\nreturn __onSuccess;`);

function run(data, input) {
  const toasts = [];
  const translations = [];
  const record = (level) => (message) => {
    toasts.push({ level, message });
  };
  const toast = { success: record('success'), warning: record('warning'), error: record('error'), info: record('info') };
  const t = (key, options) => {
    translations.push({ key, options });
    return `t:${key}`;
  };
  handlerFactory(toast, t)(data, input);
  return { toasts, translations };
}

const codexInput = { channelType: 'codex', baseURL: 'https://example.invalid' };
const clineInput = { channelType: 'cline', baseURL: 'https://example.invalid' };
const openaiInput = { channelType: 'openai', baseURL: 'https://example.invalid' };

test('a Codex catalog fallback stays silent: the dialog reports it inline', () => {
  const { toasts } = run(
    { models: [{ id: 'gpt-5' }], fallback: true, codexCatalogVersion: null, codexCatalogSkippedReason: null },
    codexInput
  );
  assert.deepEqual(toasts, [], 'Codex fallback must not raise a generic toast');
});

test('a Codex skipped catalog stays silent too', () => {
  const { toasts } = run(
    { models: [{ id: 'gpt-5' }], fallback: true, codexCatalogVersion: null, codexCatalogSkippedReason: 'no_credentials' },
    codexInput
  );
  assert.deepEqual(toasts, [], 'the skipped reason is rendered inline, not as a toast');
});

test('a Codex catalog fetch that succeeded still reports success', () => {
  const { toasts, translations } = run(
    { models: [{ id: 'gpt-5' }], fallback: false, codexCatalogVersion: null, codexCatalogSkippedReason: null },
    codexInput
  );
  assert.equal(toasts.length, 1);
  assert.equal(toasts[0].level, 'success');
  assert.equal(toasts[0].message, 't:channels.messages.fetchModelsSuccess');
  assert.equal(translations.at(-1).key, 'channels.messages.fetchModelsSuccess');
  assert.deepEqual(translations.at(-1).options, { count: 1 }, 'the success toast interpolates the model count');
});

test('a non-Codex fallback (Cline) keeps exactly one generic fallback warning', () => {
  const { toasts, translations } = run(
    { models: [{ id: 'cline-pass' }], fallback: true, codexCatalogVersion: null, codexCatalogSkippedReason: null },
    clineInput
  );
  assert.equal(toasts.length, 1, 'a non-Codex fallback must raise one warning and nothing else');
  assert.equal(toasts[0].level, 'warning');
  assert.equal(toasts[0].message, 't:channels.messages.fetchModelsFallback');
  assert.equal(translations.at(-1).key, 'channels.messages.fetchModelsFallback', 'the existing fallback key is reused');
});

test('a plain non-Codex success reports the model count', () => {
  const { toasts, translations } = run(
    { models: [{ id: 'a' }, { id: 'b' }], fallback: false, codexCatalogVersion: null, codexCatalogSkippedReason: null },
    openaiInput
  );
  assert.equal(toasts.length, 1);
  assert.equal(toasts[0].level, 'success');
  assert.equal(toasts[0].message, 't:channels.messages.fetchModelsSuccess');
  assert.deepEqual(translations.at(-1).options, { count: 2 });
});

test('a large successful list keeps the large-list success toast', () => {
  const models = Array.from({ length: 101 }, (_, i) => ({ id: `model-${i}` }));
  const { toasts, translations } = run(
    { models, fallback: false, codexCatalogVersion: null, codexCatalogSkippedReason: null },
    openaiInput
  );
  assert.equal(toasts.length, 1);
  assert.equal(toasts[0].level, 'success');
  assert.equal(toasts[0].message, 't:channels.messages.fetchModelsSuccessLarge');
  assert.deepEqual(translations.at(-1).options, { count: 101 });
});

test('a payload error still wins over fallback for every channel type', () => {
  for (const input of [codexInput, clineInput, openaiInput]) {
    const { toasts } = run(
      { models: [], error: 'upstream failed', fallback: true, codexCatalogVersion: null, codexCatalogSkippedReason: null },
      input
    );
    assert.equal(toasts.length, 1, 'an error must be the only notification');
    assert.equal(toasts[0].level, 'error');
    assert.equal(toasts[0].message, 't:common.errors.internalServerError');
  }
});

test('the fetch result type keeps the Codex catalog fields non-optional and nullable', () => {
  const mutationFnProperty = findProperty(mutationConfig, 'mutationFn');
  assert.ok(mutationFnProperty, 'useFetchModels must fetch through mutationFn');

  // Locate graphqlRequest<{ fetchModels: {...} }>() inside the real mutationFn.
  const graphqlCall = (function find(node) {
    if (ts.isCallExpression(node) && node.expression.getText(ast) === 'graphqlRequest') return node;
    return ts.forEachChild(node, find);
  })(mutationFnProperty.initializer);
  assert.ok(graphqlCall, 'mutationFn must call graphqlRequest');
  assert.equal(graphqlCall.typeArguments?.length, 1, 'graphqlRequest must be called with one response type argument');

  const responseType = graphqlCall.typeArguments[0];
  assert.ok(ts.isTypeLiteralNode(responseType), 'the graphqlRequest type argument must be an object type');
  const fetchModelsType = responseType.members.find(
    (member) => ts.isPropertySignature(member) && member.name.getText(ast) === 'fetchModels'
  );
  assert.ok(fetchModelsType, 'the response type must declare fetchModels');
  assert.ok(ts.isTypeLiteralNode(fetchModelsType.type), 'fetchModels must be an inline object type');

  for (const fieldName of ['codexCatalogVersion', 'codexCatalogSkippedReason']) {
    const field = fetchModelsType.type.members.find(
      (member) => ts.isPropertySignature(member) && member.name.getText(ast) === fieldName
    );
    assert.ok(field, `${fieldName} must stay in the fetchModels response type`);
    assert.equal(field.questionToken, undefined, `${fieldName} must be required, matching the official contract`);

    const type = field.type;
    assert.ok(type && ts.isUnionTypeNode(type), `${fieldName} must be a union type, matching the official contract`);
    const includesNull = type.types.some((part) => ts.isLiteralTypeNode(part) && part.literal.kind === ts.SyntaxKind.NullKeyword);
    assert.ok(includesNull, `${fieldName} must allow null, matching the official contract`);
  }
});
