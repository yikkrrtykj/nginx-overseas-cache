import { request } from './api.js';
import { button, form, input, message, notice } from './components.js';

void request<{ authenticated: boolean }>('session').then(reply => {
  if (reply.authenticated) location.replace('/');
}).catch(() => undefined);

form('login-form').addEventListener('submit', async event => {
  event.preventDefault();
  const submit = button('sign-in');
  submit.disabled = true;
  notice('login-error', '');
  try {
    await request('login', { username: input('username').value, password: input('password').value });
    input('password').value = '';
    location.replace('/');
  } catch (err) {
    notice('login-error', message(err));
    input('password').focus();
  } finally {
    submit.disabled = false;
  }
});
