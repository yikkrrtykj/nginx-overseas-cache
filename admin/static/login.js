const form = document.querySelector("#loginForm");
const usernameInput = document.querySelector("#username");
const passwordInput = document.querySelector("#password");
const loginButton = document.querySelector("#loginButton");
const loginError = document.querySelector("#loginError");
const togglePassword = document.querySelector("#toggleLoginPassword");

async function checkSession() {
  try {
    const response = await fetch("/api/session", { cache: "no-store", credentials: "same-origin" });
    const data = await response.json();
    if (data.authenticated) window.location.replace("/");
  } catch (_) {
    // Keep the login form available when the status check cannot complete.
  }
}

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  if (!form.reportValidity()) return;
  loginButton.disabled = true;
  loginError.textContent = "";
  try {
    const response = await fetch("/api/login", {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ username: usernameInput.value.trim(), password: passwordInput.value }),
    });
    const data = await response.json();
    if (!response.ok || !data.ok) throw new Error(data.message || "登录失败");
    passwordInput.value = "";
    window.location.replace("/");
  } catch (error) {
    loginError.textContent = error.message;
    passwordInput.select();
  } finally {
    loginButton.disabled = false;
  }
});

togglePassword.addEventListener("click", () => {
  const show = passwordInput.type === "password";
  passwordInput.type = show ? "text" : "password";
  togglePassword.textContent = show ? "隐藏" : "显示";
  passwordInput.focus();
});

checkSession();
