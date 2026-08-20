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
        join = fanout.parse_client_join(
            client,
            '{"action":"join","trxId":"trx-1","data":'
            '{"channelId":"channel-1","rendererMode":"onair"}}',
        )
        self.assertNotIn(token, join.key)
        self.assertIn("token=", join.key)
        self.assertIn(token, join.upstream_uri)
        self.assertEqual(join.upstream_origin, "https://origin.example")
        self.assertEqual(join.trx_id, "trx-1")

    def test_status_snapshot_counts_connections_without_real_token(self):
        token = "private-token-123"
        token_id = fanout.hashlib.sha256(token.encode("utf-8")).hexdigest()[:12]
        hub = fanout.Hub(
            key=f"origin=origin.example,token={token_id},channel=channel-1,mode=onair",
            upstream_uri=f"wss://origin.example/ws?token={token}",
            upstream_origin="https://origin.example",
            canonical_join='{"action":"join"}',
            upstream_host="origin.example",
            token_id=token_id,
            channel="channel-1",
            renderer_mode="onair",
        )
        clients = [object(), object(), object()]
        hub.clients.update(clients)
        hub.acknowledged.update(clients[:2])
        hub.upstream = SimpleNamespace()
        fanout.hubs[hub.key] = hub
        try:
            snapshot = fanout.status_snapshot()
        finally:
            fanout.hubs.clear()

        serialized = fanout.json.dumps(snapshot)
        self.assertNotIn(token, serialized)
        self.assertEqual(snapshot["localClients"], 3)
        self.assertEqual(snapshot["acknowledgedClients"], 2)
        self.assertEqual(snapshot["activeHubs"], 1)
        self.assertEqual(snapshot["upstreamConnections"], 1)
        self.assertEqual(snapshot["savedUpstreamConnections"], 2)


if __name__ == "__main__":
    unittest.main()
