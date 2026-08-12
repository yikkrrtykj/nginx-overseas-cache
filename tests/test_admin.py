import importlib.util
import os
import sys
import tempfile
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

    def test_update_upstream_text_changes_servers_and_maps(self):
        updated = admin.update_upstream_text(
            UPSTREAM_FIXTURE, self.current, "new-origin.example", "new-cdn.example"
        )
        self.assertNotIn("old-origin.example", updated)
        self.assertNotIn("old-cdn.example", updated)
        self.assertEqual(updated.count("new-origin.example"), 2)
        self.assertEqual(updated.count("new-cdn.example"), 2)

    def test_update_refuses_unknown_config_shape(self):
        with self.assertRaises(admin.AdminError):
            admin.update_upstream_text(
                "upstream webcoder_origin { server old-origin.example:443; }",
                self.current,
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

    def test_default_admin_networks_allow_lan_and_reject_public_addresses(self):
        networks = admin.parse_allowed_networks(None)
        self.assertTrue(admin.address_is_allowed("192.168.16.23", networks))
        self.assertTrue(admin.address_is_allowed("192.168.60.92", networks))
        self.assertTrue(admin.address_is_allowed("::ffff:192.168.60.92", networks))
        self.assertFalse(admin.address_is_allowed("8.8.8.8", networks))
        self.assertTrue(admin.host_is_allowed("192.168.16.23:23000", networks))
        self.assertFalse(admin.host_is_allowed("8.8.8.8:23000", networks))

    def test_custom_admin_networks_are_enforced(self):
        networks = admin.parse_allowed_networks("192.168.16.0/24, 127.0.0.1/32")
        self.assertTrue(admin.address_is_allowed("192.168.16.23", networks))
        self.assertFalse(admin.address_is_allowed("192.168.60.92", networks))
        with self.assertRaises(admin.AdminError):
            admin.parse_allowed_networks("not-a-network")

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


class NonceTests(unittest.TestCase):
    def test_nonce_can_only_be_used_once(self):
        store = admin.NonceStore()
        token = store.issue()
        self.assertTrue(store.consume(token))
        self.assertFalse(store.consume(token))


if __name__ == "__main__":
    unittest.main()
