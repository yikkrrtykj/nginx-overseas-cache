const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

const script = fs.readFileSync(path.join(__dirname, "../admin/static/app.js"), "utf8");
const config = {
  originHost: "origin.example.com", cdnHost: "cdn.example.com",
  channelId: "old_channel", accountId: "old_account", tokenPresent: true,
};
const result = {
  sourceHost: "origin.example.com", recommendedHost: "cdn.example.com",
  channelId: "new_channel", accountId: "new_account", token: "new-token-123",
  inspectedCount: 1, warnings: [], candidates: [],
};

class Element {
  constructor() {
    this.value = "";
    this.children = [];
    this.listeners = new Map();
    this.classList = { add() {}, remove() {} };
  }
  addEventListener(type, callback) {
    this.listeners.set(type, [...(this.listeners.get(type) || []), callback]);
  }
  async dispatch(type) {
    for (const callback of this.listeners.get(type) || []) await callback({ preventDefault() {} });
  }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = children; }
  querySelector() { return this.child ||= new Element(); }
  querySelectorAll() { return this.children; }
  scrollIntoView() {}
}

const settle = () => new Promise((resolve) => setImmediate(resolve));

async function consoleHarness() {
  const elements = new Map();
  const element = (selector) => {
    if (!elements.has(selector)) elements.set(selector, new Element());
    return elements.get(selector);
  };
  const calls = [];
  const harness = { element, calls, discoveryResult: result, discoveryReply: null };
  const response = (payload) => ({ ok: true, status: 200, json: async () => payload });
  const fetch = async (url, options = {}) => {
    calls.push({ url, options });
    if (url === "/api/status") return response({ ok: true, status: { available: false } });
    if (url === "/api/discover") {
      if (harness.discoveryReply) return harness.discoveryReply;
      return response({ ok: true, result: harness.discoveryResult });
    }
    if (url === "/api/config" && options.method === "POST") {
      return response({ ok: true, config: JSON.parse(options.body), message: "已保存" });
    }
    return response({ ok: true, config, csrfToken: "test-nonce" });
  };
  for (const name of ["originHost", "cdnHost", "channelId", "accountId"]) {
    element(`#${name}`).required = true;
  }
  element("#configForm").reportValidity = () => ["originHost", "cdnHost", "channelId", "accountId", "token"]
    .every((name) => !element(`#${name}`).required || element(`#${name}`).value.trim());
  element("#discoveryForm").reportValidity = () => true;
  element("#confirmDialog").showModal = () => {
    queueMicrotask(() => {
      element("#confirmDialog").returnValue = "confirm";
      element("#confirmDialog").dispatch("close");
    });
  };
  vm.runInNewContext(script, {
    document: { querySelector: element, createElement: () => new Element() }, fetch,
    window: { clearTimeout() {}, setTimeout() { return 1; }, location: { replace() {} } },
  });
  await settle();
  element("#sourceUrl").value = "https://origin.example.com/render?channelId=new_channel&accountId=new_account&token=new-token-123";
  return harness;
}

test("complete URL automatically fills the new channel and saves all matching parameters", async () => {
  const { element, calls } = await consoleHarness();
  await element("#discoveryForm").dispatch("submit");
  assert.equal(element("#channelId").value, "new_channel");
  assert.equal(element("#accountId").value, "new_account");
  assert.equal(element("#token").value, "new-token-123");
  assert.equal(element("#discoveredChannel").textContent, "new_channel");
  assert.equal(element("#discoveredToken").textContent, "已识别（隐藏）");
  assert.equal(calls.filter((call) => call.options.method === "POST" && call.url === "/api/config").length, 0);
  await element("#configForm").dispatch("submit");
  const saved = calls.find((call) => call.url === "/api/config" && call.options.method === "POST");
  assert.deepEqual(JSON.parse(saved.options.body), {
    originHost: result.sourceHost, cdnHost: result.recommendedHost,
    channelId: result.channelId, accountId: result.accountId, token: result.token,
  });
  const summary = element("#changeSummary").children.map((row) => row.children[1].textContent);
  assert.ok(summary.includes("old_channel → new_channel"));
  assert.ok(!summary.includes(result.token));
});

test("missing channel, account or token clears old input and blocks saving until supplied", async () => {
  for (const missing of ["channelId", "accountId", "token"]) {
    const harness = await consoleHarness();
    harness.element("#token").value = "typed-old-token";
    harness.discoveryResult = { ...result, [missing]: "" };
    await harness.element("#discoveryForm").dispatch("submit");
    assert.equal(harness.element(`#${missing}`).value, "");
    assert.equal(harness.element("#token").required, true);
    await harness.element("#configForm").dispatch("submit");
    assert.equal(harness.calls.some((call) => call.url === "/api/config" && call.options.method === "POST"), false);
    assert.ok(harness.element("#statusMessage").textContent.includes(missing));
  }
});

test("channel still updates when no CDN is discovered; old CDN is not silently reused", async () => {
  const harness = await consoleHarness();
  harness.discoveryResult = { ...result, recommendedHost: "" };
  await harness.element("#discoveryForm").dispatch("submit");
  assert.equal(harness.element("#channelId").value, "new_channel");
  assert.equal(harness.element("#cdnHost").value, "");
  assert.equal(harness.element("#applyDiscoveryButton").disabled, false);
});

test("editing the source while scanning discards the older response", async () => {
  const harness = await consoleHarness();
  let resolveReply;
  harness.discoveryReply = new Promise((resolve) => { resolveReply = resolve; });
  const pending = harness.element("#discoveryForm").dispatch("submit");
  harness.element("#sourceUrl").value = "https://origin.example.com/render?channelId=another_channel";
  await harness.element("#sourceUrl").dispatch("input");
  resolveReply({ ok: true, status: 200, json: async () => ({ ok: true, result }) });
  await pending;
  assert.equal(harness.element("#channelId").value, "old_channel");
  assert.equal(harness.element("#discoveryResults").hidden, true);
  assert.equal(harness.element("#applyDiscoveryButton").disabled, true);
  assert.equal(harness.element("#statusTitle").textContent, "地址已经修改");
});

test("changing a previously identified URL blocks saving its outdated parameters", async () => {
  const harness = await consoleHarness();
  await harness.element("#discoveryForm").dispatch("submit");
  harness.element("#sourceUrl").value = "https://origin.example.com/render?channelId=another_channel";
  await harness.element("#sourceUrl").dispatch("input");
  await harness.element("#configForm").dispatch("submit");
  assert.equal(harness.calls.some((call) => call.url === "/api/config" && call.options.method === "POST"), false);
  assert.equal(harness.element("#statusTitle").textContent, "请重新识别当前地址");
});

test("saving while discovery is pending cannot submit the old channel", async () => {
  const harness = await consoleHarness();
  let resolveReply;
  harness.discoveryReply = new Promise((resolve) => { resolveReply = resolve; });
  const pending = harness.element("#discoveryForm").dispatch("submit");
  await harness.element("#configForm").dispatch("submit");
  assert.equal(harness.calls.some((call) => call.url === "/api/config" && call.options.method === "POST"), false);
  assert.equal(harness.element("#statusTitle").textContent, "请等待识别完成");
  resolveReply({ ok: true, status: 200, json: async () => ({ ok: true, result }) });
  await pending;
  assert.equal(harness.element("#channelId").value, "new_channel");
});
