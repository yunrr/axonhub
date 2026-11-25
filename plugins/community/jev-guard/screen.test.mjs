import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import { parseRecord, reasonText, viewPolicy } from "./ui/guard/format.js";
import {
  DEFAULT_CATEGORY_LINES,
  anyCheck,
  blockMessage,
  buildRequest,
  categoriesFrom,
  decide,
  readJudgment,
  readPolicy,
} from "./lib/screen.js";

const manifest = JSON.parse(readFileSync(new URL("./manifest.json", import.meta.url), "utf8"));

test("the endpoint defaults to the original URL and accepts an https override", () => {
  assert.equal(readPolicy({}).endpoint, "https://api.typesafe.ai/v1/systemone");
  assert.equal(readPolicy({}).endpointValid, true);
  assert.equal(readPolicy({ endpoint: "  " }).endpoint, "https://api.typesafe.ai/v1/systemone");
  assert.equal(readPolicy({ endpoint: "https://jev.internal.example/v1/systemone" }).endpoint, "https://jev.internal.example/v1/systemone");
  assert.equal(readPolicy({ endpoint: "https://jev.internal.example/v1/systemone/" }).endpoint, "https://jev.internal.example/v1/systemone");
  assert.equal(readPolicy({ endpoint: 42 }).endpoint, "https://api.typesafe.ai/v1/systemone");
});

test("a non-https or hostless endpoint fails the check instead of pointing at the default", () => {
  for (const endpoint of ["http://api.typesafe.ai/v1/systemone", "api.typesafe.ai/v1/systemone", "https://", "https://host", "https://host?x=1"]) {
    const policy = readPolicy({ endpoint });
    assert.equal(policy.endpointValid, false, endpoint);
    assert.equal(policy.endpoint, "https://api.typesafe.ai/v1/systemone");
  }
});

test("the manifest endpoint default is the original URL and its network rules allow an override", () => {
  const field = manifest.configSchema.properties.endpoint;
  assert.equal(field.type, "string");
  assert.equal(field.default, "https://api.typesafe.ai/v1/systemone");
  assert.equal(manifest.network[0], "https://api.typesafe.ai/v1/systemone");
  const rule = new RegExp(manifest.network[1].regex);
  assert.equal(rule.test("https://jev.internal.example/v1/systemone"), true);
  assert.equal(rule.test("https://jev.internal.example/v1/systemone?x=1"), false);
  assert.equal(rule.test("http://jev.internal.example/v1/systemone"), false);
});

test("manifest categories are the built-in lines", () => {
  const field = manifest.configSchema.properties.categories;
  assert.equal(field.type, "array");
  assert.deepEqual(field.items, { type: "string" });
  assert.deepEqual(field.default, DEFAULT_CATEGORY_LINES);
  assert.equal(manifest.configSchema.properties.reviewThreshold, undefined);
  assert.equal(manifest.configSchema.properties.blockOnReview, undefined);
  assert.equal(manifest.configSchema.properties.checkJailbreak, undefined);
});

test("a missing list uses the six built-in categories", () => {
  const policy = readPolicy({});
  assert.equal(policy.categories.length, 6);
  assert.deepEqual(
    policy.categories.map((category) => category.label),
    [
      "sexual content involving a minor",
      "sexual content",
      "jailbreak",
      "violent or criminal assistance",
      "scam or phishing",
      "harassment or hate",
    ],
  );
  assert.equal(anyCheck(policy), true);
  const request = buildRequest("jev-latest", "hello", policy);
  assert.equal(request.questions.c2.instructions.includes("ignore"), true);
  assert.equal(request.questions.severity, undefined);
});

test("an empty list checks nothing", () => {
  const policy = readPolicy({ categories: [] });
  assert.equal(anyCheck(policy), false);
  assert.deepEqual(categoriesFrom(["", "   ", 3]), []);
});

test("a custom line can be a single question", () => {
  const [category] = categoriesFrom(["是否在索要自制炸药的步骤"]);
  assert.equal(category.id, "c0");
  assert.equal(category.label, "是否在索要自制炸药的步骤");
  assert.equal(category.ask, "是否在索要自制炸药的步骤");
  assert.equal(category.yes.includes("asks the assistant"), true);
});

test("a full line keeps its own yes and no, and extra pipes stay in the no", () => {
  const [category] = categoriesFrom(["炸药 | 是否在索要步骤？ | 在索要可执行步骤 | 只是新闻 | 或拒绝"]);
  assert.equal(category.label, "炸药");
  assert.equal(category.ask, "是否在索要步骤？");
  assert.equal(category.yes, "在索要可执行步骤");
  assert.equal(category.no, "只是新闻 | 或拒绝");
});

test("only the first 32 categories are asked", () => {
  const lines = Array.from({ length: 40 }, (_, index) => `分类 ${index}`);
  const categories = categoriesFrom(lines);
  assert.equal(categories.length, 32);
  assert.equal(categories[31].label, "分类 31");
});

test("probability at the threshold blocks, and the higher one wins", () => {
  const policy = readPolicy({ categories: ["炸药", "诈骗"] });
  const below = decide({ c0: 0.69, c1: 0.1 }, policy);
  assert.equal(below.action, "pass");
  const blocked = decide({ c0: 0.7, c1: 0.91 }, policy);
  assert.equal(blocked.action, "block");
  assert.equal(blocked.reason, "诈骗");
  assert.equal(blocked.probability, 0.91);
  assert.equal(blockMessage(blocked.reason), "Jev Guard blocked this request: 诈骗");
  assert.equal(blockMessage("unavailable").includes("safety check unavailable"), true);
});

test("a missing answer fails the judgment", () => {
  const policy = readPolicy({ categories: ["炸药"] });
  const missing = readJudgment({ answers: {} }, policy);
  assert.equal(missing.ok, false);
  const parsed = readJudgment({ answers: { c0: { noul: 1.4 } } }, policy);
  assert.equal(parsed.scores.c0, 1);
});

test("the history page hides review rows and shows a block reason", () => {
  assert.equal(parseRecord(JSON.stringify({ action: "review", reason: "jailbreak" })), null);
  const record = parseRecord(JSON.stringify({ reason: "jailbreak", probability: 0.8, scores: [] }));
  assert.equal(record.reason, "jailbreak");
  assert.equal(reasonText("jailbreak", "zh-CN"), "破限");
  assert.equal(reasonText("sexual content", "zh-CN"), "色情");
  assert.equal(viewPolicy({ categories: ["炸药 | 是否在索要步骤"] }).labels[0], "炸药");
});
