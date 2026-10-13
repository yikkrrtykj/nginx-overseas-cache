const form = document.querySelector("#configForm");
const reloadButton = document.querySelector("#reloadButton");
const saveButton = document.querySelector("#saveButton");
const toggleToken = document.querySelector("#toggleToken");
const tokenInput = document.querySelector("#token");
const tokenHint = document.querySelector("#tokenHint");
const dialog = document.querySelector("#confirmDialog");
const changeSummary = document.querySelector("#changeSummary");
const statusPanel = document.querySelector(".status-panel");
const statusIcon = document.querySelector("#statusIcon");
const statusTitle = document.querySelector("#statusTitle");
const statusMessage = document.querySelector("#statusMessage");
const passwordButton = document.querySelector("#passwordButton");
const logoutButton = document.querySelector("#logoutButton");
const passwordDialog = document.querySelector("#passwordDialog");
const passwordForm = document.querySelector("#passwordForm");
const currentPassword = document.querySelector("#currentPassword");
const newPassword = document.querySelector("#newPassword");
const confirmPassword = document.querySelector("#confirmPassword");
const passwordError = document.querySelector("#passwordError");
const submitPassword = document.querySelector("#submitPassword");
const cancelPassword = document.querySelector("#cancelPassword");
const discoveryForm = document.querySelector("#discoveryForm");
const sourceUrl = document.querySelector("#sourceUrl");
const evidenceUrl = document.querySelector("#evidenceUrl");
const discoverButton = document.querySelector("#discoverButton");
const discoveryResults = document.querySelector("#discoveryResults");
const discoveredOrigin = document.querySelector("#discoveredOrigin");
const discoveredCdn = document.querySelector("#discoveredCdn");
const discoveredCount = document.querySelector("#discoveredCount");
const discoveredChannel = document.querySelector("#discoveredChannel");
const discoveredAccount = document.querySelector("#discoveredAccount");
const discoveredToken = document.querySelector("#discoveredToken");
const discoveryWarnings = document.querySelector("#discoveryWarnings");
const candidateList = document.querySelector("#candidateList");
const applyDiscoveryButton = document.querySelector("#applyDiscoveryButton");
const telemetryBadge = document.querySelector("#telemetryBadge");
const upstreamConnectionCount = document.querySelector("#upstreamConnectionCount");
const localConnectionCount = document.querySelector("#localConnectionCount");
const acknowledgedConnectionCount = document.querySelector("#acknowledgedConnectionCount");
const activeHubCount = document.querySelector("#activeHubCount");
const savedConnectionCount = document.querySelector("#savedConnectionCount");
const connectionRows = document.querySelector("#connectionRows");
const telemetryUpdatedAt = document.querySelector("#telemetryUpdatedAt");

const fields = ["originHost", "cdnHost", "channelId", "accountId"];
let csrfToken = "";
let latestDiscovery = null;
let selectedDiscoveryHost = "";
let telemetryTimer = null;
let loadedConfig = null;
let appliedDiscoveryInput = null;

async function parseResponse(response) {
  const data = await response.json();
  if (response.status === 401) {
    window.location.replace("/login");
    throw new Error("登录已过期");
  }
  return data;
}

function setStatus(kind, title, message) {
  statusPanel.className = `status-panel ${kind || ""}`.trim();
  statusIcon.textContent = kind === "success" ? "✓" : kind === "error" ? "!" : "i";
  statusTitle.textContent = title;
  statusMessage.textContent = message;
}

function connectionCell(text, className = "") {
  const cell = document.createElement("td");
  cell.textContent = text;
  if (className) cell.className = className;
  return cell;
}

function connectionEmpty(message) {
  const row = document.createElement("tr");
  const cell = connectionCell(message, "connection-empty");
  cell.colSpan = 5;
  row.append(cell);
  return row;
}

function renderConnectionStatus(status) {
  telemetryBadge.className = `telemetry-badge ${status.available ? "online" : "offline"}`;
  telemetryBadge.querySelector("strong").textContent = status.available ? "实时更新" : "状态不可用";

  if (!status.available) {
    upstreamConnectionCount.textContent = "—";
    localConnectionCount.textContent = "—";
    acknowledgedConnectionCount.textContent = status.message || "fanout 暂未上报";
    activeHubCount.textContent = "—";
    savedConnectionCount.textContent = "—";
    connectionRows.replaceChildren(connectionEmpty(status.message || "fanout 暂未上报状态"));
    telemetryUpdatedAt.textContent = "等待状态恢复";
    return;
  }

  upstreamConnectionCount.textContent = status.upstreamConnections;
  localConnectionCount.textContent = status.localClients;
  acknowledgedConnectionCount.textContent = `已就绪 ${status.acknowledgedClients}`;
  activeHubCount.textContent = status.activeHubs;
  savedConnectionCount.textContent = status.savedUpstreamConnections;

  const rows = [];
  for (const group of status.groups || []) {
    const row = document.createElement("tr");
    const identity = document.createElement("td");
    const origin = document.createElement("strong");
    const channel = document.createElement("small");
    origin.textContent = group.origin || "未知上游";
    channel.textContent = group.channel || "未提供频道";
    identity.append(origin, channel);

    const upstreamState = group.upstreamConnected ? "已连接" : "正在重连";
    row.append(
      identity,
      connectionCell(group.mode || "—"),
      connectionCell(group.tokenId || "—", "token-id"),
      connectionCell(`${group.localClients}（就绪 ${group.acknowledgedClients}）`),
      connectionCell(upstreamState, group.upstreamConnected ? "state-online" : "state-waiting")
    );
    rows.push(row);
  }
  connectionRows.replaceChildren(...(rows.length ? rows : [connectionEmpty("当前没有本地渲染连接")]));

  const updated = new Date(status.generatedAt);
  telemetryUpdatedAt.textContent = Number.isNaN(updated.getTime())
    ? "刚刚更新"
    : `更新于 ${updated.toLocaleTimeString("zh-CN", { hour12: false })}`;
}

async function readConnectionStatus() {
  if (telemetryTimer) window.clearTimeout(telemetryTimer);
  try {
    const response = await fetch("/api/status", { cache: "no-store", credentials: "same-origin" });
    const data = await parseResponse(response);
    if (!response.ok || !data.ok) throw new Error(data.message || "读取线路状态失败");
    renderConnectionStatus(data.status);
  } catch (error) {
    renderConnectionStatus({ available: false, message: error.message });
  } finally {
    telemetryTimer = window.setTimeout(readConnectionStatus, 3000);
  }
}

function currentPayload() {
  const payload = Object.fromEntries(fields.map((name) => [name, document.querySelector(`#${name}`).value.trim()]));
  payload.token = tokenInput.value.trim();
  return payload;
}

function fillForm(config) {
  loadedConfig = { ...config };
  appliedDiscoveryInput = null;
  fields.forEach((name) => { document.querySelector(`#${name}`).value = config[name] || ""; });
  tokenInput.value = "";
  tokenInput.required = false;
  tokenInput.placeholder = "留空表示保持当前 token 不变";
  tokenHint.textContent = "手动修改时 token 留空可保持原值；识别新网页后必须填写该网页的 token。";
}

async function readConfig() {
  reloadButton.disabled = true;
  setStatus("", "正在读取服务器配置", "旧 token 不会返回浏览器。");
  try {
    const response = await fetch("/api/config", { cache: "no-store", credentials: "same-origin" });
    const data = await parseResponse(response);
    if (data.csrfToken) csrfToken = data.csrfToken;
    if (!response.ok || !data.ok) throw new Error(data.message || "读取失败");
    fillForm(data.config);
    setStatus("success", "配置已就绪", "修改后点击“保存并应用”。留空 token 将保持服务器上的原值。 ");
  } catch (error) {
    setStatus("error", "无法读取配置", error.message);
  } finally {
    reloadButton.disabled = false;
  }
}

function summaryRow(label, value) {
  const row = document.createElement("p");
  const title = document.createElement("span");
  const code = document.createElement("code");
  title.textContent = label;
  code.textContent = value;
  row.append(title, code);
  return row;
}

function confirmChanges(payload) {
  const parameterSummary = (name) => {
    const previous = loadedConfig?.[name];
    return previous && previous !== payload[name] ? `${previous} → ${payload[name]}` : payload[name];
  };
  changeSummary.replaceChildren(
    summaryRow("主站域名", payload.originHost),
    summaryRow("CDN 域名", payload.cdnHost),
    summaryRow("channelId", parameterSummary("channelId")),
    summaryRow("accountId", parameterSummary("accountId")),
    summaryRow("token", payload.token ? "替换为新值" : "保持当前值")
  );
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), { once: true });
  });
}

async function saveConfig(payload) {
  saveButton.disabled = true;
  setStatus("", "正在验证并应用", "服务器正在备份旧配置并运行 Nginx 检查，请不要关闭页面。");
  try {
    const response = await fetch("/api/config", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
      body: JSON.stringify(payload),
    });
    const data = await parseResponse(response);
    if (data.csrfToken) csrfToken = data.csrfToken;
    if (!response.ok || !data.ok) throw new Error(data.message || "保存失败");
    fillForm(data.config);
    const backup = data.backup ? `备份编号：${data.backup}` : "没有需要写入的变化。";
    setStatus("success", "配置已生效", `${data.message}\n${backup}`);
  } catch (error) {
    const failureMessage = error.message;
    await readConfig();
    setStatus("error", "没有应用新配置", `${failureMessage}\n服务器已重新读取当前有效配置。`);
  } finally {
    saveButton.disabled = false;
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (discoverButton.disabled) {
    setStatus("", "请等待识别完成", "新网页参数正在识别，完成后再保存。");
    return;
  }
  if (appliedDiscoveryInput && (
    sourceUrl.value.trim() !== appliedDiscoveryInput.source ||
    evidenceUrl.value.trim() !== appliedDiscoveryInput.evidence
  )) {
    setStatus("error", "请重新识别当前地址", "地址已修改，上次识别的频道参数不能用于保存当前网页。");
    return;
  }
  if (!form.reportValidity()) return;
  const payload = currentPayload();
  if (!(await confirmChanges(payload))) return;
  await saveConfig(payload);
});

reloadButton.addEventListener("click", readConfig);
toggleToken.addEventListener("click", () => {
  const show = tokenInput.type === "password";
  tokenInput.type = show ? "text" : "password";
  toggleToken.textContent = show ? "隐藏" : "显示";
  tokenInput.focus();
});

function candidateCard(candidate, selected) {
  const label = document.createElement("label");
  label.className = `candidate-card${selected ? " selected" : ""}`;

  const radio = document.createElement("input");
  radio.type = "radio";
  radio.name = "cdnCandidate";
  radio.value = candidate.host;
  radio.checked = selected;
  radio.addEventListener("change", () => {
    selectedDiscoveryHost = candidate.host;
    discoveredCdn.textContent = candidate.host;
    applyDiscoveryButton.disabled = !candidate.compatible;
    document.querySelector("#cdnHost").value = candidate.compatible ? candidate.host : "";
    candidateList.querySelectorAll(".candidate-card").forEach((card) => card.classList.remove("selected"));
    label.classList.add("selected");
  });

  const content = document.createElement("div");
  const heading = document.createElement("div");
  heading.className = "candidate-title";
  const host = document.createElement("strong");
  host.textContent = candidate.host;
  const verification = document.createElement("span");
  verification.className = candidate.valid ? "verification verified" : "verification";
  verification.textContent = candidate.valid
    ? `${candidate.compatible ? "已验证" : "路径待适配"} · HTTP ${candidate.status}`
    : candidate.verified
      ? `响应异常 · HTTP ${candidate.status}`
      : "扫描发现";
  heading.append(host, verification);

  const details = document.createElement("p");
  const typeText = candidate.types.length ? candidate.types.join(" / ") : "未知资源";
  const sampleText = candidate.samples.length ? candidate.samples[0] : "/";
  details.textContent = `${typeText} · ${candidate.contentType || "待验证"} · ${sampleText}`;
  content.append(heading, details);
  label.append(radio, content);
  return label;
}

function renderDiscovery(result) {
  latestDiscovery = result;
  selectedDiscoveryHost = result.recommendedHost || "";
  discoveredOrigin.textContent = result.sourceHost || "未识别";
  discoveredCdn.textContent = selectedDiscoveryHost || "未识别";
  discoveredCount.textContent = `${result.inspectedCount} 项`;
  discoveredChannel.textContent = result.channelId || "未识别，需填写";
  discoveredAccount.textContent = result.accountId || "未识别，需填写";
  discoveredToken.textContent = result.token ? "已识别（隐藏）" : "未识别，需填写";
  discoveryWarnings.replaceChildren();
  for (const warningText of result.warnings || []) {
    const warning = document.createElement("p");
    warning.textContent = warningText;
    discoveryWarnings.append(warning);
  }
  candidateList.replaceChildren();
  for (const candidate of result.candidates || []) {
    candidateList.append(candidateCard(candidate, candidate.host === selectedDiscoveryHost));
  }
  if (!result.candidates?.length) {
    const empty = document.createElement("p");
    empty.className = "candidate-empty";
    empty.textContent = "没有找到可用资源域名，请补充 F12 中成功资源的请求地址后重新识别。";
    candidateList.append(empty);
  }
  applyDiscoveryButton.disabled = !result.sourceHost;
  discoveryResults.hidden = false;
  discoveryResults.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

discoveryForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!discoveryForm.reportValidity()) return;
  const submittedSource = sourceUrl.value.trim();
  const submittedEvidence = evidenceUrl.value.trim();
  latestDiscovery = null;
  selectedDiscoveryHost = "";
  discoverButton.disabled = true;
  discoveryResults.hidden = true;
  setStatus("", "正在识别网页资源", "Ubuntu 正在加载原始页面并检查静态资源域名，通常需要几秒。 ");
  try {
    const response = await fetch("/api/discover", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
      body: JSON.stringify({ sourceUrl: submittedSource, evidenceUrl: submittedEvidence }),
    });
    const data = await parseResponse(response);
    if (data.csrfToken) csrfToken = data.csrfToken;
    if (!response.ok || !data.ok) throw new Error(data.message || "识别失败");
    if (sourceUrl.value.trim() !== submittedSource || evidenceUrl.value.trim() !== submittedEvidence) {
      setStatus("", "地址已经修改", "本次结果未填入，请重新识别当前地址。");
      return;
    }
    renderDiscovery(data.result);
    applyDiscovery(false);
  } catch (error) {
    setStatus("error", "无法识别网页", error.message);
  } finally {
    discoverButton.disabled = false;
  }
});

function applyDiscovery(scroll = true) {
  if (!latestDiscovery) return;
  appliedDiscoveryInput = { source: sourceUrl.value.trim(), evidence: evidenceUrl.value.trim() };
  document.querySelector("#originHost").value = latestDiscovery.sourceHost || "";
  document.querySelector("#cdnHost").value = selectedDiscoveryHost;
  document.querySelector("#channelId").value = latestDiscovery.channelId || "";
  document.querySelector("#accountId").value = latestDiscovery.accountId || "";
  tokenInput.value = latestDiscovery.token || "";
  tokenInput.required = true;
  tokenInput.placeholder = "请填写新网页地址中的 token";
  tokenHint.textContent = latestDiscovery.token
    ? "已填入新网页的 token，将与新频道一起保存。"
    : "未识别到新网页的 token，请补填后保存；不会自动沿用旧 token。";
  if (scroll) form.scrollIntoView({ behavior: "smooth", block: "start" });
  const missing = [
    ...fields.filter((name) => !document.querySelector(`#${name}`).value),
    ...(tokenInput.value ? [] : ["token"]),
  ];
  setStatus(
    missing.length ? "" : "success",
    missing.length ? "识别结果已填入，待补全" : "识别结果已自动填入",
    missing.length
      ? `请补全 ${missing.join("、")} 后保存；未识别的参数已清空。`
      : `新频道：${latestDiscovery.channelId}。请核对识别提示，再点击“保存并应用”；服务器当前配置尚未改变。`
  );
}

applyDiscoveryButton.addEventListener("click", () => applyDiscovery());

[sourceUrl, evidenceUrl].forEach((input) => {
  input.addEventListener("input", () => {
    latestDiscovery = null;
    selectedDiscoveryHost = "";
    discoveryResults.hidden = true;
    applyDiscoveryButton.disabled = true;
    if (appliedDiscoveryInput) {
      setStatus("", "地址已经修改", "请重新识别，新结果会更新上方的频道参数。");
    }
  });
});

passwordButton.addEventListener("click", () => {
  passwordForm.reset();
  passwordError.textContent = "";
  passwordDialog.showModal();
  currentPassword.focus();
});

cancelPassword.addEventListener("click", () => passwordDialog.close());

passwordForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  passwordError.textContent = "";
  if (!passwordForm.reportValidity()) return;
  if (newPassword.value !== confirmPassword.value) {
    passwordError.textContent = "两次输入的新密码不一致";
    confirmPassword.focus();
    return;
  }
  submitPassword.disabled = true;
  try {
    const response = await fetch("/api/password", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
      body: JSON.stringify({
        currentPassword: currentPassword.value,
        newPassword: newPassword.value,
      }),
    });
    const data = await parseResponse(response);
    if (data.csrfToken) csrfToken = data.csrfToken;
    if (!response.ok || !data.ok) throw new Error(data.message || "密码修改失败");
    passwordForm.reset();
    window.location.replace("/login");
  } catch (error) {
    passwordError.textContent = error.message;
  } finally {
    submitPassword.disabled = false;
  }
});

logoutButton.addEventListener("click", async () => {
  logoutButton.disabled = true;
  try {
    await fetch("/api/logout", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
  } finally {
    window.location.replace("/login");
  }
});

readConfig();
readConnectionStatus();
