const form = document.querySelector("#configForm");
const reloadButton = document.querySelector("#reloadButton");
const saveButton = document.querySelector("#saveButton");
const toggleToken = document.querySelector("#toggleToken");
const tokenInput = document.querySelector("#token");
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
  fields.forEach((name) => { document.querySelector(`#${name}`).value = config[name] || ""; });
  tokenInput.value = "";
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
  changeSummary.replaceChildren(
    summaryRow("主站域名", payload.originHost),
    summaryRow("CDN 域名", payload.cdnHost),
    summaryRow("channelId", payload.channelId),
    summaryRow("accountId", payload.accountId),
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
  applyDiscoveryButton.disabled = !result.sourceHost || !selectedDiscoveryHost;
  discoveryResults.hidden = false;
  discoveryResults.scrollIntoView({ behavior: "smooth", block: "nearest" });
}

discoveryForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!discoveryForm.reportValidity()) return;
  discoverButton.disabled = true;
  discoveryResults.hidden = true;
  setStatus("", "正在识别网页资源", "Ubuntu 正在加载原始页面并检查静态资源域名，通常需要几秒。 ");
  try {
    const response = await fetch("/api/discover", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
      body: JSON.stringify({ sourceUrl: sourceUrl.value.trim(), evidenceUrl: evidenceUrl.value.trim() }),
    });
    const data = await parseResponse(response);
    if (data.csrfToken) csrfToken = data.csrfToken;
    if (!response.ok || !data.ok) throw new Error(data.message || "识别失败");
    renderDiscovery(data.result);
    const note = data.result.warnings?.length
      ? "识别完成，但仍有需要确认的项目。查看下方提示后再填入配置。"
      : "已找到并验证资源域名，可以填入上方配置。";
    setStatus("success", "网页识别完成", note);
  } catch (error) {
    setStatus("error", "无法识别网页", error.message);
  } finally {
    discoverButton.disabled = false;
  }
});

applyDiscoveryButton.addEventListener("click", () => {
  if (!latestDiscovery || !selectedDiscoveryHost) return;
  document.querySelector("#originHost").value = latestDiscovery.sourceHost || "";
  document.querySelector("#cdnHost").value = selectedDiscoveryHost;
  if (latestDiscovery.channelId) document.querySelector("#channelId").value = latestDiscovery.channelId;
  if (latestDiscovery.accountId) document.querySelector("#accountId").value = latestDiscovery.accountId;
  if (latestDiscovery.token) tokenInput.value = latestDiscovery.token;
  form.scrollIntoView({ behavior: "smooth", block: "start" });
  setStatus(
    "success",
    "识别结果已填入",
    "请核对主站、CDN 和参数；点击“保存并应用”后才会修改服务器配置。"
  );
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
