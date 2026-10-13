import importlib.util
import http.client
import json
import os
import sys
import tempfile
import threading
import types
import unittest
from pathlib import Path


# fcntl exists on the Ubuntu deployment target but not on Windows, where these
# repository tests run. Configuration parsing tests only need an import shim.
if "fcntl" not in sys.modules:
    fake_fcntl = types.ModuleType("fcntl")
    fake_fcntl.LOCK_EX = 2
    fake_fcntl.flock = lambda *_args: None
    sys.modules["fcntl"] = fake_fcntl


MODULE_PATH = Path(__file__).resolve().parents[1] / "admin" / "server.py"
SPEC = importlib.util.spec_from_file_location("overseas_cache_admin", MODULE_PATH)
admin = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = admin
SPEC.loader.exec_module(admin)


UPSTREAM_FIXTURE = """\
upstream webcoder_origin {
    server old-origin.example:443;
    keepalive 64;
}

upstream webcoder_cdn {
    server old-cdn.example:443;
    keepalive 128;
}

map $host $overseas_origin_host {
    default old-origin.example;
}

map $host $overseas_cdn_host {
    default old-cdn.example;
}
"""


class AdminConfigTests(unittest.TestCase):
    def setUp(self):
        self.current = admin.CurrentConfig(
            "old-origin.example", "old-cdn.example", "channel_1", "account.1", "secret-token"
        )

    def test_parse_upstreams(self):
        self.assertEqual(admin.parse_upstream_host(UPSTREAM_FIXTURE, "webcoder_origin"), "old-origin.example")
        self.assertEqual(admin.parse_upstream_host(UPSTREAM_FIXTURE, "webcoder_cdn"), "old-cdn.example")

    def test_fanout_status_file_reports_fresh_and_stale_data(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "status.json"
            path.write_text(
                json.dumps(
                    {
                        "generatedAt": "2026-08-20T07:32:36.953Z",
                        "uptimeSeconds": 123,
                        "localClients": 15,
                        "acknowledgedClients": 15,
                        "activeHubs": 1,
                        "upstreamConnections": 1,
                        "savedUpstreamConnections": 14,
                        "groups": [
                            {
                                "origin": "origin.example",
                                "tokenId": "e75970beff73",
                                "channel": "channel-1",
                                "mode": "onair",
                                "localClients": 15,
                                "acknowledgedClients": 15,
                                "upstreamConnected": True,
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            modified = path.stat().st_mtime
            fresh = admin.read_fanout_status(path, now=modified + 2)
            stale = admin.read_fanout_status(path, now=modified + 20)

        self.assertTrue(fresh["available"])
        self.assertEqual(fresh["localClients"], 15)
        self.assertEqual(fresh["upstreamConnections"], 1)
        self.assertEqual(fresh["groups"][0]["tokenId"], "e75970beff73")
        self.assertFalse(stale["available"])

    def test_update_upstream_text_changes_servers_and_maps(self):
        updated = admin.update_upstream_text(
            UPSTREAM_FIXTURE, "new-origin.example", "new-cdn.example"
        )
        self.assertNotIn("old-origin.example", updated)
        self.assertNotIn("old-cdn.example", updated)
        self.assertEqual(updated.count("new-origin.example"), 2)
        self.assertEqual(updated.count("new-cdn.example"), 2)

    def test_update_refuses_unknown_config_shape(self):
        with self.assertRaises(admin.AdminError):
            admin.update_upstream_text(
                "upstream webcoder_origin { server old-origin.example:443; }",
                "new-origin.example",
                "new-cdn.example",
            )

    def test_hostname_validation(self):
        self.assertEqual(admin.validate_hostname("API.Example.COM.", "主站"), "api.example.com")
        for value in ("https://api.example.com", "api.example.com/path", "api.example.com:443", "localhost"):
            with self.subTest(value=value), self.assertRaises(admin.AdminError):
                admin.validate_hostname(value, "主站")

    def test_id_and_token_validation_blocks_url_injection(self):
        self.assertEqual(admin.validate_id("pgs26_p", "channelId"), "pgs26_p")
        self.assertEqual(admin.validate_token("", "existing-token"), "existing-token")
        self.assertEqual(admin.validate_token("padded-token==", "existing-token"), "padded-token==")
        with self.assertRaises(admin.AdminError):
            admin.validate_id("channel&token=stolen", "channelId")
        with self.assertRaises(admin.AdminError):
            admin.validate_token("bad token&x=1", "existing-token")

    def test_discovery_url_normalization_extracts_url_and_rejects_unsafe_targets(self):
        self.assertEqual(
            admin.normalize_discovery_url(
                "请求 URL: https://CDN.Example.com/assets/font.woff2?v=1", "资源地址"
            ),
            "https://cdn.example.com/assets/font.woff2?v=1",
        )
        for value in (
            "http://cdn.example.com/font.woff2",
            "https://127.0.0.1/font.woff2",
            "https://user@cdn.example.com/font.woff2",
            "https://cdn.example.com:8443/font.woff2",
        ):
            with self.subTest(value=value), self.assertRaises(admin.AdminError):
                admin.normalize_discovery_url(value, "资源地址")

    def test_public_address_validation_rejects_private_dns_answers(self):
        original_getaddrinfo = admin.socket.getaddrinfo
        try:
            admin.socket.getaddrinfo = lambda *_args, **_kwargs: [
                (admin.socket.AF_INET, admin.socket.SOCK_STREAM, 6, "", ("192.168.16.23", 443))
            ]
            with self.assertRaisesRegex(admin.AdminError, "禁止扫描"):
                admin.resolve_public_addresses("internal.example")
            admin.socket.getaddrinfo = lambda *_args, **_kwargs: [
                (admin.socket.AF_INET, admin.socket.SOCK_STREAM, 6, "", ("93.184.216.34", 443))
            ]
            self.assertEqual(
                admin.resolve_public_addresses("public.example"), ("93.184.216.34",)
            )
        finally:
            admin.socket.getaddrinfo = original_getaddrinfo

    def test_resource_extraction_and_render_parameter_parsing(self):
        html = """
        <link rel="stylesheet" href="https://cdn.example.com/templates/main.css">
        <script src="/static/app.js"></script>
        <img srcset="//img.example.com/team.png 1x, //img.example.com/team@2x.png 2x">
        """
        urls = admin.extract_resource_urls(html, "https://origin.example.com/render", "text/html")
        self.assertIn("https://cdn.example.com/templates/main.css", urls)
        self.assertIn("https://origin.example.com/static/app.js", urls)
        self.assertIn("https://img.example.com/team.png", urls)
        params = admin.parse_render_parameters(
            "https://origin.example.com/render?rendererMode=onair&channelId=pgs26_p&token=secure-token-123&accountId=pgs26p"
        )
        self.assertEqual(
            params,
            {"channelId": "pgs26_p", "accountId": "pgs26p", "token": "secure-token-123"},
        )
        self.assertEqual(
            admin.safe_sample_path(
                "https://origin.example.com/templates-auth/channel_1/secret-token-123/board/index.html"
            ),
            "/templates-auth/channel_1/<token>/board/index.html",
        )

    def test_discovery_accepts_html_escaped_render_query(self):
        source_url = admin.normalize_discovery_url(
            "https://origin.example.com/render?rendererMode=onair&amp;channelId=new_channel"
            "&amp;accountId=new_account&amp;token=new-token-123",
            "原始网页地址",
        )
        self.assertEqual(
            admin.parse_render_parameters(source_url),
            {"channelId": "new_channel", "accountId": "new_account", "token": "new-token-123"},
        )

    def test_discovery_uses_parameters_from_redirect_destination(self):
        from unittest.mock import patch

        for source_url in (
            "https://origin.example.com/short-link",
            "https://origin.example.com/render?channelId=old_channel&accountId=old_account&token=old-token-123",
        ):
            with self.subTest(source_url=source_url):
                destination = admin.DiscoveryFetch(
                    "https://origin.example.com/render?channelId=new_channel&accountId=new_account&token=new-token-123",
                    200, "text/html", b"",
                )
                with patch.object(admin, "fetch_public_url", return_value=destination):
                    result = admin.discover_site(source_url)
                self.assertEqual(result["channelId"], "new_channel")
                self.assertEqual(result["accountId"], "new_account")
                self.assertEqual(result["token"], "new-token-123")

    def test_redirect_to_new_channel_does_not_reuse_old_token(self):
        from unittest.mock import patch

        destination = admin.DiscoveryFetch(
            "https://origin.example.com/render?channelId=new_channel",
            200, "text/html", b"",
        )
        with patch.object(admin, "fetch_public_url", return_value=destination):
            result = admin.discover_site(
                "https://origin.example.com/render?channelId=old_channel&accountId=old_account&token=old-token-123"
            )
        self.assertEqual(result["channelId"], "new_channel")
        self.assertEqual(result["accountId"], "")
        self.assertEqual(result["token"], "")
        self.assertTrue(any("accountId、token" in warning for warning in result["warnings"]))

    def test_redirect_without_parameters_preserves_explicit_source_parameters(self):
        from unittest.mock import patch

        destination = admin.DiscoveryFetch("https://origin.example.com/render", 200, "text/html", b"")
        with patch.object(admin, "fetch_public_url", return_value=destination):
            result = admin.discover_site(
                "https://origin.example.com/render?channelId=new_channel&accountId=new_account&token=new-token-123"
            )
        self.assertEqual(result["channelId"], "new_channel")
        self.assertEqual(result["accountId"], "new_account")
        self.assertEqual(result["token"], "new-token-123")

    def test_discovery_ranks_verified_f12_resource_as_cdn(self):
        source = admin.DiscoveryFetch(
            "https://origin.example.com/render?channelId=channel_1&accountId=account_1&token=secure-token-123",
            200,
            "text/html",
            b'<link rel="stylesheet" href="https://cdn.example.com/templates/main.css">',
        )
        stylesheet = admin.DiscoveryFetch(
            "https://cdn.example.com/templates/main.css",
            200,
            "text/css",
            b'@font-face { src: url("https://cdn.example.com/templates/font.woff2"); }',
        )
        font = admin.DiscoveryFetch(
            "https://cdn.example.com/templates/font.woff2",
            200,
            "font/woff2",
            b"font-data",
        )
        original_fetch = admin.fetch_public_url
        try:
            def fake_fetch(url, maximum=admin.DISCOVERY_MAX_DOCUMENT):
                del maximum
                path = admin.urlsplit(url).path
                if path.endswith("main.css"):
                    return stylesheet
                if path.endswith("font.woff2"):
                    return font
                return source

            admin.fetch_public_url = fake_fetch
            result = admin.discover_site(source.url, font.url)
        finally:
            admin.fetch_public_url = original_fetch
        self.assertEqual(result["sourceHost"], "origin.example.com")
        self.assertEqual(result["recommendedHost"], "cdn.example.com")
        self.assertEqual(result["channelId"], "channel_1")
        self.assertEqual(result["token"], "secure-token-123")
        self.assertTrue(result["candidates"][0]["valid"])

    def test_discovery_does_not_recommend_incompatible_asset_path(self):
        source = admin.DiscoveryFetch(
            "https://origin.example.com/render?channelId=channel_1&accountId=account_1&token=secure-token-123",
            200,
            "text/html",
            b'<script src="https://assets.example.com/static/app.js"></script>',
        )
        script = admin.DiscoveryFetch(
            "https://assets.example.com/static/app.js", 200, "application/javascript", b""
        )
        original_fetch = admin.fetch_public_url
        try:
            admin.fetch_public_url = lambda url, maximum=admin.DISCOVERY_MAX_DOCUMENT: (
                script if admin.urlsplit(url).path.endswith("app.js") else source
            )
            result = admin.discover_site(source.url)
        finally:
            admin.fetch_public_url = original_fetch
        self.assertEqual(result["recommendedHost"], "")
        self.assertTrue(any("路径" in warning for warning in result["warnings"]))

    def test_default_admin_networks_allow_lan_and_reject_public_addresses(self):
        networks = admin.parse_allowed_networks(None)
        self.assertTrue(admin.address_is_allowed("192.168.16.23", networks))
        self.assertTrue(admin.address_is_allowed("192.168.60.92", networks))
        self.assertTrue(admin.address_is_allowed("::ffff:192.168.60.92", networks))
        self.assertFalse(admin.address_is_allowed("8.8.8.8", networks))
        self.assertTrue(admin.host_is_allowed("192.168.16.23:23000", networks))
        self.assertFalse(admin.host_is_allowed("8.8.8.8:23000", networks))
        self.assertFalse(admin.host_is_allowed("user@192.168.16.23:23000", networks))
        self.assertFalse(admin.host_is_allowed("192.168.16.23:23000/path", networks))

    def test_custom_admin_networks_are_enforced(self):
        networks = admin.parse_allowed_networks("192.168.16.0/24, 127.0.0.1/32")
        self.assertTrue(admin.address_is_allowed("192.168.16.23", networks))
        self.assertFalse(admin.address_is_allowed("192.168.60.92", networks))
        with self.assertRaises(admin.AdminError):
            admin.parse_allowed_networks("not-a-network")
        with self.assertRaises(admin.AdminError):
            admin.parse_allowed_networks("")

    def test_same_origin_validation_is_strict(self):
        networks = admin.parse_allowed_networks(None)
        self.assertTrue(
            admin.same_origin_is_allowed(
                "http://192.168.16.23:23000", "192.168.16.23:23000", networks
            )
        )
        self.assertTrue(
            admin.same_origin_is_allowed("http://192.168.16.23", "192.168.16.23:80", networks)
        )
        for origin in (
            "https://192.168.16.23:23000",
            "http://192.168.60.92:23000",
            "http://user@192.168.16.23:23000",
            "http://192.168.16.23:23000/path",
        ):
            with self.subTest(origin=origin):
                self.assertFalse(
                    admin.same_origin_is_allowed(origin, "192.168.16.23:23000", networks)
                )

    def test_nginx_safe_log_does_not_record_request_paths(self):
        config = (MODULE_PATH.parents[1] / "nginx" / "conf.d" / "proxy-cache.conf").read_text(
            encoding="utf-8"
        )
        log_format = config.split("log_format overseas_cache_safe", 1)[1].split(";", 1)[0]
        self.assertNotIn("$uri", log_format)
        self.assertNotIn("$request_uri", log_format)
        self.assertIn("$overseas_log_route", log_format)

    def test_nginx_routes_websocket_through_local_fanout(self):
        config = (
            MODULE_PATH.parents[1] / "nginx" / "sites-available" / "overseas-cache"
        ).read_text(encoding="utf-8")
        location = config.split("location = /ws {", 1)[1].split("\n    }", 1)[0]
        self.assertIn("proxy_pass http://127.0.0.1:3001;", location)
        self.assertIn('X-Fanout-Upstream-Base "wss://$overseas_origin_host/ws";', location)
        self.assertIn('X-Fanout-Upstream-Origin "https://$overseas_origin_host";', location)
        self.assertIn("proxy_cache off;", location)
        self.assertIn("proxy_buffering off;", location)

    def test_settings_round_trip_and_no_shell_syntax(self):
        content = admin.render_settings("channel_1", "account-1", "secure_token-123")
        self.assertEqual(
            admin.parse_settings(content),
            ("channel_1", "account-1", "secure_token-123"),
        )
        self.assertNotIn("export ", content)

    def test_atomic_write_sets_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "settings.conf"
            original_chown = getattr(admin.os, "chown", None)
            admin.os.chown = lambda *_args: None
            try:
                admin.atomic_write(target, "value\n", 0o600)
            finally:
                if original_chown is None:
                    delattr(admin.os, "chown")
                else:
                    admin.os.chown = original_chown
            self.assertEqual(target.read_text(encoding="utf-8"), "value\n")
            if os.name == "posix":
                self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_apply_config_writes_all_values_and_keeps_token_when_blank(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = {
                "UPSTREAM_CONFIG": root / "proxy-cache.conf",
                "SITE_CONFIG": root / "overseas-cache",
                "SETTINGS_CONFIG": root / "settings.conf",
                "BACKUP_ROOT": root / "backups",
                "LOCK_FILE": root / "admin.lock",
            }
            originals = {name: getattr(admin, name) for name in paths}
            original_chown = getattr(admin.os, "chown", None)
            original_run_checked = admin.run_checked
            try:
                for name, path in paths.items():
                    setattr(admin, name, path)
                paths["UPSTREAM_CONFIG"].write_text(UPSTREAM_FIXTURE, encoding="utf-8")
                paths["SITE_CONFIG"].write_text("server {}\n", encoding="utf-8")
                paths["SETTINGS_CONFIG"].write_text(
                    admin.render_settings("channel_1", "account.1", "secret-token"), encoding="utf-8"
                )
                admin.os.chown = lambda *_args: None
                admin.run_checked = lambda *_args, **_kwargs: "syntax is ok"

                config, backup, _ = admin.apply_config(
                    {
                        "originHost": "new-origin.example",
                        "cdnHost": "new-cdn.example",
                        "channelId": "channel_2",
                        "accountId": "account.2",
                        "token": "",
                    }
                )

                self.assertIsNotNone(backup)
                self.assertEqual(config.token, "secret-token")
                self.assertEqual(
                    admin.parse_settings(paths["SETTINGS_CONFIG"].read_text(encoding="utf-8")),
                    ("channel_2", "account.2", "secret-token"),
                )
                updated = paths["UPSTREAM_CONFIG"].read_text(encoding="utf-8")
                self.assertIn("new-origin.example", updated)
                self.assertIn("new-cdn.example", updated)
            finally:
                for name, value in originals.items():
                    setattr(admin, name, value)
                admin.run_checked = original_run_checked
                if original_chown is None:
                    delattr(admin.os, "chown")
                else:
                    admin.os.chown = original_chown

    def test_apply_config_reports_failed_rollback(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            paths = {
                "UPSTREAM_CONFIG": root / "proxy-cache.conf",
                "SITE_CONFIG": root / "overseas-cache",
                "SETTINGS_CONFIG": root / "settings.conf",
                "BACKUP_ROOT": root / "backups",
                "LOCK_FILE": root / "admin.lock",
            }
            originals = {name: getattr(admin, name) for name in paths}
            original_chown = getattr(admin.os, "chown", None)
            original_run_checked = admin.run_checked
            try:
                for name, path in paths.items():
                    setattr(admin, name, path)
                paths["UPSTREAM_CONFIG"].write_text(UPSTREAM_FIXTURE, encoding="utf-8")
                paths["SITE_CONFIG"].write_text("server {}\n", encoding="utf-8")
                paths["SETTINGS_CONFIG"].write_text(
                    admin.render_settings("channel_1", "account.1", "secret-token"), encoding="utf-8"
                )
                admin.os.chown = lambda *_args: None
                admin.run_checked = lambda *_args, **_kwargs: (_ for _ in ()).throw(
                    admin.AdminError("simulated nginx failure")
                )

                with self.assertRaisesRegex(admin.AdminError, "自动恢复未能完成"):
                    admin.apply_config(
                        {
                            "originHost": "new-origin.example",
                            "cdnHost": "new-cdn.example",
                            "channelId": "channel_2",
                            "accountId": "account.2",
                            "token": "",
                        }
                    )
            finally:
                for name, value in originals.items():
                    setattr(admin, name, value)
                admin.run_checked = original_run_checked
                if original_chown is None:
                    delattr(admin.os, "chown")
                else:
                    admin.os.chown = original_chown


class NonceTests(unittest.TestCase):
    def test_nonce_can_only_be_used_once(self):
        store = admin.NonceStore()
        token = store.issue()
        self.assertTrue(store.consume(token))
        self.assertFalse(store.consume(token))


class AuthenticationTests(unittest.TestCase):
    def test_password_record_verification_and_validation(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "admin-password.json"
            path.write_text(admin.render_password_record("admin", "correct-horse-123"), encoding="utf-8")
            self.assertTrue(admin.verify_credentials("admin", "correct-horse-123", path))
            self.assertFalse(admin.verify_credentials("admin", "wrong-password-123", path))
            self.assertFalse(admin.verify_credentials("other", "correct-horse-123", path))
            record = path.read_text(encoding="utf-8")
            self.assertNotIn("correct-horse-123", record)
            self.assertIn("pbkdf2-sha256", record)
        with self.assertRaises(admin.AdminError):
            admin.render_password_record("admin", "too-short")

    def test_session_store_can_revoke_one_or_all_sessions(self):
        store = admin.SessionStore()
        first = store.issue("admin")
        second = store.issue("admin")
        self.assertTrue(store.valid(first))
        self.assertTrue(store.valid(second))
        store.revoke(first)
        self.assertFalse(store.valid(first))
        self.assertTrue(store.valid(second))
        store.revoke_all()
        self.assertFalse(store.valid(second))

    def test_change_password_replaces_hash_and_rejects_old_password(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "admin-password.json"
            path.write_text(admin.render_password_record("admin", "old-password-123"), encoding="utf-8")
            original_path = admin.PASSWORD_CONFIG
            original_chown = getattr(admin.os, "chown", None)
            admin.PASSWORD_CONFIG = path
            admin.os.chown = lambda *_args: None
            try:
                admin.change_password("old-password-123", "new-password-456")
                self.assertFalse(admin.verify_credentials("admin", "old-password-123", path))
                self.assertTrue(admin.verify_credentials("admin", "new-password-456", path))
            finally:
                admin.PASSWORD_CONFIG = original_path
                if original_chown is None:
                    delattr(admin.os, "chown")
                else:
                    admin.os.chown = original_chown

    def test_login_limiter_blocks_after_five_failures(self):
        limiter = admin.LoginLimiter()
        address = "192.168.16.50"
        for _ in range(5):
            self.assertTrue(limiter.allowed(address)[0])
            limiter.fail(address)
        allowed, retry_after = limiter.allowed(address)
        self.assertFalse(allowed)
        self.assertGreater(retry_after, 0)
        limiter.success(address)
        self.assertTrue(limiter.allowed(address)[0])

    def test_http_login_session_and_logout_flow(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "admin-password.json"
            status_path = Path(directory) / "status.json"
            path.write_text(admin.render_password_record("admin", "login-password-123"), encoding="utf-8")
            status_path.write_text(
                json.dumps(
                    {
                        "localClients": 15,
                        "acknowledgedClients": 15,
                        "activeHubs": 1,
                        "upstreamConnections": 1,
                        "savedUpstreamConnections": 14,
                        "groups": [],
                    }
                ),
                encoding="utf-8",
            )
            original_path = admin.PASSWORD_CONFIG
            original_status_path = admin.FANOUT_STATUS
            original_sessions = admin.SESSIONS
            original_limiter = admin.LOGIN_LIMITER
            original_nonces = admin.NONCES
            original_discover = admin.discover_site
            admin.PASSWORD_CONFIG = path
            admin.FANOUT_STATUS = status_path
            admin.SESSIONS = admin.SessionStore()
            admin.LOGIN_LIMITER = admin.LoginLimiter()
            admin.NONCES = admin.NonceStore()
            admin.discover_site = lambda source, evidence="": {
                "sourceHost": "origin.example.com",
                "recommendedHost": "cdn.example.com",
                "source": source,
                "evidence": evidence,
            }
            server = admin.AdminHTTPServer(("127.0.0.1", 0), admin.AdminHandler)
            server.allowed_networks = admin.parse_allowed_networks(None)
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            connection = http.client.HTTPConnection("127.0.0.1", server.server_port, timeout=5)
            host = f"127.0.0.1:{server.server_port}"
            try:
                connection.request("GET", "/")
                response = connection.getresponse()
                response.read()
                self.assertEqual(response.status, 303)
                self.assertEqual(response.getheader("Location"), "/login")

                body = json.dumps({"username": "admin", "password": "login-password-123"})
                connection.request(
                    "POST",
                    "/api/login",
                    body=body,
                    headers={"Content-Type": "application/json", "Origin": f"http://{host}"},
                )
                response = connection.getresponse()
                response.read()
                self.assertEqual(response.status, 200)
                cookie = response.getheader("Set-Cookie").split(";", 1)[0]

                connection.request("GET", "/api/session", headers={"Cookie": cookie})
                response = connection.getresponse()
                session_payload = json.loads(response.read())
                self.assertTrue(session_payload["authenticated"])

                connection.request("GET", "/api/status", headers={"Cookie": cookie})
                response = connection.getresponse()
                status_payload = json.loads(response.read())
                self.assertEqual(response.status, 200)
                self.assertEqual(status_payload["status"]["localClients"], 15)
                self.assertEqual(status_payload["status"]["upstreamConnections"], 1)

                csrf_token = admin.NONCES.issue()
                connection.request(
                    "POST",
                    "/api/discover",
                    body=json.dumps(
                        {
                            "sourceUrl": "https://origin.example.com/render",
                            "evidenceUrl": "https://cdn.example.com/templates/font.woff2",
                        }
                    ),
                    headers={
                        "Content-Type": "application/json",
                        "Origin": f"http://{host}",
                        "Cookie": cookie,
                        "X-CSRF-Token": csrf_token,
                    },
                )
                response = connection.getresponse()
                discovery_payload = json.loads(response.read())
                self.assertEqual(response.status, 200)
                self.assertEqual(discovery_payload["result"]["recommendedHost"], "cdn.example.com")
                self.assertTrue(discovery_payload["csrfToken"])

                connection.request(
                    "POST",
                    "/api/logout",
                    body="{}",
                    headers={
                        "Content-Type": "application/json",
                        "Origin": f"http://{host}",
                        "Cookie": cookie,
                    },
                )
                response = connection.getresponse()
                response.read()
                self.assertEqual(response.status, 200)

                connection.request("GET", "/api/session", headers={"Cookie": cookie})
                response = connection.getresponse()
                session_payload = json.loads(response.read())
                self.assertFalse(session_payload["authenticated"])
            finally:
                connection.close()
                server.shutdown()
                server.server_close()
                thread.join(timeout=5)
                admin.PASSWORD_CONFIG = original_path
                admin.FANOUT_STATUS = original_status_path
                admin.SESSIONS = original_sessions
                admin.LOGIN_LIMITER = original_limiter
                admin.NONCES = original_nonces
                admin.discover_site = original_discover


if __name__ == "__main__":
    unittest.main()
