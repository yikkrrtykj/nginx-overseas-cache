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

const fields = ["originHost", "cdnHost", "channelId", "accountId"];
let csrfToken = "";

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
