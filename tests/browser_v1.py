"""Run the preserved V1 Admin with isolated credentials and status."""
import hashlib
import json
import os
import sys
import tempfile
import types
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "admin"))
if sys.platform == "win32":
    # V1 applies flock/chown only on Linux. Browser comparison reads files.
    sys.modules["fcntl"] = types.ModuleType("fcntl")
import server


def main():
    with tempfile.TemporaryDirectory() as tmp:
        base = Path(tmp)
        server.PASSWORD_CONFIG = base / "password.json"
        server.PASSWORD_CONFIG.write_text(
            server.render_password_record("admin", "Local browser fixture password"), encoding="utf-8"
        )
        server.UPSTREAM_CONFIG = ROOT / "nginx" / "conf.d" / "proxy-cache.conf"
        server.SETTINGS_CONFIG = base / "settings.conf"
        server.SETTINGS_CONFIG.write_text(
            server.render_settings("fixture", "renderer", "synthetic-token-01"), encoding="utf-8"
        )
        server.FANOUT_STATUS = base / "status.json"

        read_status = server.read_fanout_status

        def status():
            server.FANOUT_STATUS.write_text(json.dumps({
                "version": 1,
                "generatedAt": datetime.now(timezone.utc).isoformat(),
                "uptimeSeconds": 10905,
                "localClients": 29,
                "acknowledgedClients": 28,
                "activeHubs": 3,
                "upstreamConnections": 3,
                "savedUpstreamConnections": 26,
                "groups": [
                    {"origin": host, "channel": channel, "mode": mode, "localClients": total,
                     "acknowledgedClients": confirmed, "upstreamConnected": True,
                     "tokenId": hashlib.sha256(b"browser fixture token id").hexdigest()[:12]}
                    for host, channel, mode, total, confirmed in [
                        ("origin.example", "arena-stage", "onair", 18, 18),
                        ("origin.example", "observer-feed", "preview", 7, 7),
                        ("backup.example", "arena-stage", "onair", 4, 3),
                    ]
                ]
            }), encoding="utf-8")
            return read_status()

        server.read_fanout_status = status
        httpd = server.AdminHTTPServer(("127.0.0.1", 0), server.AdminHandler)
        httpd.allowed_networks = server.parse_allowed_networks(None)
        print(f"BROWSER_READY 127.0.0.1:{httpd.server_port}", flush=True)
        httpd.serve_forever()


if __name__ == "__main__":
    main()
