import importlib.util
import sys
import unittest
from pathlib import Path
from types import SimpleNamespace


MODULE_PATH = Path(__file__).resolve().parents[1] / "ws-fanout" / "ws_fanout.py"
SPEC = importlib.util.spec_from_file_location("ws_fanout", MODULE_PATH)
fanout = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = fanout
SPEC.loader.exec_module(fanout)


class UpstreamValidationTests(unittest.TestCase):
    def test_valid_upstream_is_normalized(self):
        self.assertEqual(
            fanout.validated_upstream(
                "wss://WebCoder.Example/ws", "https://webcoder.example"
            ),
            (
                "wss://webcoder.example/ws",
                "https://webcoder.example",
                "webcoder.example",
            ),
        )

    def test_upstream_requires_same_https_hostname(self):
        invalid = (
            ("ws://origin.example/ws", "https://origin.example"),
            ("wss://origin.example/ws", "http://origin.example"),
            ("wss://origin.example/ws", "https://other.example"),
            ("wss://127.0.0.1/ws", "https://127.0.0.1"),
            ("wss://user@origin.example/ws", "https://origin.example"),
            ("wss://origin.example:8443/ws", "https://origin.example"),
            ("wss://origin.example/ws?other=1", "https://origin.example"),
        )
        for base, origin in invalid:
            with self.subTest(base=base, origin=origin), self.assertRaises(ValueError):
                fanout.validated_upstream(base, origin)

    def test_logs_identify_token_by_hash_only(self):
        token = "private-token-123"
        client = SimpleNamespace(
            request=SimpleNamespace(
                path=f"/ws?token={token}",
                headers={
                    fanout.UPSTREAM_BASE_HEADER: "wss://origin.example/ws",
                    fanout.UPSTREAM_ORIGIN_HEADER: "https://origin.example",
                },
            )
        )
        key, uri, origin, trx_id = fanout.parse_client_join(
            client,
            '{"action":"join","trxId":"trx-1","data":'
            '{"channelId":"channel-1","rendererMode":"onair"}}',
        )
        self.assertNotIn(token, key)
        self.assertIn("token=", key)
        self.assertIn(token, uri)
        self.assertEqual(origin, "https://origin.example")
        self.assertEqual(trx_id, "trx-1")


if __name__ == "__main__":
    unittest.main()
