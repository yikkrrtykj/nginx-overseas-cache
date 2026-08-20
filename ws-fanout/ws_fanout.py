#!/usr/bin/env python3
"""Share one upstream renderer WebSocket among matching LAN clients."""

import asyncio
import copy
import hashlib
import ipaddress
import json
import logging
import os
import re
import time

from collections import OrderedDict
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional
from urllib.parse import parse_qs, urlencode, urlsplit

from websockets.asyncio.client import ClientConnection, connect
from websockets.asyncio.server import ServerConnection, serve
from websockets.exceptions import ConnectionClosed


LISTEN_HOST = os.getenv("LISTEN_HOST", "127.0.0.1")
LISTEN_PORT = int(os.getenv("LISTEN_PORT", "3001"))
DEFAULT_UPSTREAM_BASE = os.getenv("UPSTREAM_BASE", "")
DEFAULT_UPSTREAM_ORIGIN = os.getenv("UPSTREAM_ORIGIN", "")
LOG_LEVEL = os.getenv("LOG_LEVEL", "INFO").upper()
STATUS_FILE = Path(os.getenv("STATUS_FILE", "/run/ws-fanout/status.json"))
STATUS_INTERVAL = max(1.0, float(os.getenv("STATUS_INTERVAL", "2")))
UPSTREAM_BASE_HEADER = "X-Fanout-Upstream-Base"
UPSTREAM_ORIGIN_HEADER = "X-Fanout-Upstream-Origin"
HOST_PATTERN = re.compile(
    r"(?=.{1,253}\Z)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+"
    r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\Z"
)

logging.basicConfig(
    level=getattr(logging, LOG_LEVEL, logging.INFO),
    format="%(asctime)s %(levelname)s %(message)s",
)
log = logging.getLogger("ws-fanout")
started_at = time.monotonic()


@dataclass(frozen=True)
class ClientJoin:
    key: str
    upstream_uri: str
    upstream_origin: str
    trx_id: str
    upstream_host: str
    token_id: str
    channel: str
    renderer_mode: str


@dataclass
class Hub:
    key: str
    upstream_uri: str
    upstream_origin: str
    canonical_join: str | bytes
    upstream_host: str
    token_id: str
    channel: str
    renderer_mode: str

    clients: set[ServerConnection] = field(default_factory=set)
    join_trx: dict[ServerConnection, str] = field(default_factory=dict)
    acknowledged: set[ServerConnection] = field(default_factory=set)

    leader: Optional[ServerConnection] = None
    upstream: Optional[ClientConnection] = None
    upstream_task: Optional[asyncio.Task] = None

    join_response_template: Optional[dict] = None
    latest_call: Optional[str | bytes] = None

    send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    seen_call_res: OrderedDict[str, float] = field(default_factory=OrderedDict)


hubs: dict[str, Hub] = {}
hubs_lock = asyncio.Lock()


def parse_json(message: str | bytes) -> Optional[dict]:
    try:
        text = message.decode("utf-8") if isinstance(message, bytes) else message
        value = json.loads(text)
        return value if isinstance(value, dict) else None
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None


def utc_timestamp() -> str:
    return (
        datetime.now(timezone.utc)
        .isoformat(timespec="milliseconds")
        .replace("+00:00", "Z")
    )


def validated_upstream(base: str, origin: str) -> tuple[str, str, str]:
    """Accept only a conventional public-hostname WSS endpoint on port 443."""
    try:
        parsed_base = urlsplit(base)
        parsed_origin = urlsplit(origin)
        base_port = parsed_base.port or 443
        origin_port = parsed_origin.port or 443
    except ValueError as exc:
        raise ValueError("invalid fanout upstream address") from exc

    base_host = (parsed_base.hostname or "").lower().rstrip(".")
    origin_host = (parsed_origin.hostname or "").lower().rstrip(".")
    try:
        ipaddress.ip_address(base_host)
    except ValueError:
        base_is_ip = False
    else:
        base_is_ip = True
    if (
        parsed_base.scheme != "wss"
        or parsed_origin.scheme != "https"
        or base_port != 443
        or origin_port != 443
        or not HOST_PATTERN.fullmatch(base_host)
        or base_is_ip
        or base_host != origin_host
        or parsed_base.username is not None
        or parsed_base.password is not None
        or parsed_origin.username is not None
        or parsed_origin.password is not None
        or parsed_base.query
        or parsed_base.fragment
        or parsed_origin.path not in ("", "/")
        or parsed_origin.query
        or parsed_origin.fragment
    ):
        raise ValueError("unsupported fanout upstream address")

    path = parsed_base.path or "/ws"
    if not path.startswith("/"):
        raise ValueError("invalid fanout upstream path")
    normalized_base = f"wss://{base_host}{path}"
    normalized_origin = f"https://{origin_host}"
    return normalized_base, normalized_origin, base_host


def client_upstream(client: ServerConnection) -> tuple[str, str, str]:
    request = client.request
    headers = request.headers if request is not None else {}
    base = headers.get(UPSTREAM_BASE_HEADER, DEFAULT_UPSTREAM_BASE)
    origin = headers.get(UPSTREAM_ORIGIN_HEADER, DEFAULT_UPSTREAM_ORIGIN)
    if not base or not origin:
        raise ValueError("Nginx did not provide the fanout upstream")
    return validated_upstream(base, origin)


def parse_client_join(
    client: ServerConnection,
    first_message: str | bytes,
) -> ClientJoin:
    payload = parse_json(first_message)
    if not payload or payload.get("action") != "join":
        raise ValueError("first WebSocket message must be action=join")

    data = payload.get("data")
    if not isinstance(data, dict):
        data = {}

    channel = str(data.get("channelId") or payload.get("channel") or "")
    renderer_mode = str(data.get("rendererMode") or "")
    trx_id = str(payload.get("trxId") or "")

    if not trx_id:
        raise ValueError("join message is missing trxId")

    request_path = client.request.path if client.request else "/ws"
    parsed = urlsplit(request_path)
    query = parse_qs(parsed.query)

    token = str((query.get("token") or [data.get("token") or ""])[0])
    if not token:
        raise ValueError("missing WebSocket token")

    upstream_base, upstream_origin, upstream_host = client_upstream(client)
    token_id = hashlib.sha256(token.encode("utf-8")).hexdigest()[:12]
    key = (
        f"origin={upstream_host},token={token_id},"
        f"channel={channel},mode={renderer_mode}"
    )
    upstream_uri = f"{upstream_base}?{urlencode({'token': token})}"

    return ClientJoin(
        key=key,
        upstream_uri=upstream_uri,
        upstream_origin=upstream_origin,
        trx_id=trx_id,
        upstream_host=upstream_host,
        token_id=token_id,
        channel=channel,
        renderer_mode=renderer_mode,
    )


def status_snapshot() -> dict[str, object]:
    groups = []
    for hub in sorted(hubs.values(), key=lambda item: item.key):
        groups.append(
            {
                "origin": hub.upstream_host,
                "tokenId": hub.token_id,
                "channel": hub.channel,
                "mode": hub.renderer_mode,
                "localClients": len(hub.clients),
                "acknowledgedClients": len(hub.acknowledged),
                "upstreamConnected": hub.upstream is not None,
            }
        )

    local_clients = sum(int(group["localClients"]) for group in groups)
    acknowledged_clients = sum(
        int(group["acknowledgedClients"]) for group in groups
    )
    upstream_connections = sum(
        1 for group in groups if group["upstreamConnected"]
    )
    return {
        "version": 1,
        "generatedAt": utc_timestamp(),
        "uptimeSeconds": max(0, int(time.monotonic() - started_at)),
        "localClients": local_clients,
        "acknowledgedClients": acknowledged_clients,
        "activeHubs": len(groups),
        "upstreamConnections": upstream_connections,
        "savedUpstreamConnections": max(0, local_clients - upstream_connections),
        "groups": groups,
    }


def write_status_file() -> None:
    temporary = STATUS_FILE.with_name(f".{STATUS_FILE.name}.{os.getpid()}.tmp")
    try:
        STATUS_FILE.parent.mkdir(parents=True, exist_ok=True)
        temporary.write_text(
            json.dumps(status_snapshot(), ensure_ascii=False, separators=(",", ":")),
            encoding="utf-8",
        )
        os.chmod(temporary, 0o644)
        os.replace(temporary, STATUS_FILE)
    except OSError as exc:
        log.warning("could not publish status file: %s", exc)
        try:
            temporary.unlink(missing_ok=True)
        except OSError:
            pass


async def status_loop() -> None:
    while True:
        write_status_file()
        await asyncio.sleep(STATUS_INTERVAL)


async def send_join_response(hub: Hub, client: ServerConnection) -> None:
    if client in hub.acknowledged:
        return

    template = hub.join_response_template
    trx_id = hub.join_trx.get(client)

    if template is None or not trx_id:
        return

    response = copy.deepcopy(template)
    response["trxId"] = trx_id
    response["timestamp"] = utc_timestamp()

    await client.send(
        json.dumps(response, ensure_ascii=False, separators=(",", ":"))
    )
    hub.acknowledged.add(client)

    log.info("local join acknowledged hub=%s clients=%d", hub.key, len(hub.clients))

    # A new browser must receive join_res before the latest complete frame.
    if hub.latest_call is not None:
        await client.send(hub.latest_call)


async def acknowledge_pending_clients(hub: Hub) -> None:
    for client in list(hub.clients):
        if client in hub.acknowledged:
            continue
        try:
            await send_join_response(hub, client)
        except Exception:
            hub.clients.discard(client)
            hub.join_trx.pop(client, None)
            hub.acknowledged.discard(client)


async def broadcast(hub: Hub, message: str | bytes) -> None:
    clients = [client for client in list(hub.clients) if client in hub.acknowledged]
    if not clients:
        return

    results = await asyncio.gather(
        *(client.send(message) for client in clients), return_exceptions=True
    )

    for client, result in zip(clients, results):
        if isinstance(result, Exception):
            hub.clients.discard(client)
            hub.join_trx.pop(client, None)
            hub.acknowledged.discard(client)
            if hub.leader is client:
                hub.leader = next(iter(hub.clients), None)


async def send_upstream(hub: Hub, message: str | bytes) -> None:
    async with hub.send_lock:
        if hub.upstream is None:
            return
        try:
            await hub.upstream.send(message)
        except ConnectionClosed:
            pass


def first_call_response(hub: Hub, trx_id: str) -> bool:
    if trx_id in hub.seen_call_res:
        return False

    hub.seen_call_res[trx_id] = time.monotonic()
    while len(hub.seen_call_res) > 2048:
        hub.seen_call_res.popitem(last=False)
    return True


async def upstream_loop(hub: Hub) -> None:
    backoff = 1

    try:
        while hub.clients:
            try:
                log.info("connecting upstream hub=%s", hub.key)

                async with connect(
                    hub.upstream_uri,
                    origin=hub.upstream_origin,
                    proxy=None,
                    compression="deflate",
                    open_timeout=10,
                    ping_interval=20,
                    ping_timeout=20,
                    close_timeout=5,
                    max_size=None,
                    max_queue=64,
                ) as upstream:
                    hub.upstream = upstream
                    hub.join_response_template = None
                    await upstream.send(hub.canonical_join)

                    log.info(
                        "upstream connected hub=%s clients=%d",
                        hub.key,
                        len(hub.clients),
                    )
                    backoff = 1

                    async for message in upstream:
                        payload = parse_json(message)
                        action = payload.get("action") if payload else None

                        if action == "join_res":
                            hub.join_response_template = payload
                            await acknowledge_pending_clients(hub)
                            continue

                        if action == "call":
                            hub.latest_call = message

                        await broadcast(hub, message)

            except asyncio.CancelledError:
                raise
            except Exception:
                log.exception("upstream failed; retry in %ss", backoff)
            finally:
                hub.upstream = None

            if hub.clients:
                await asyncio.sleep(backoff)
                backoff = min(backoff * 2, 15)
    finally:
        hub.upstream = None


async def remove_client(hub: Hub, client: ServerConnection) -> None:
    hub.clients.discard(client)
    hub.join_trx.pop(client, None)
    hub.acknowledged.discard(client)

    if hub.leader is client:
        hub.leader = next(iter(hub.clients), None)

    log.info("client disconnected hub=%s clients=%d", hub.key, len(hub.clients))

    if hub.clients:
        return

    task_to_cancel: Optional[asyncio.Task] = None
    async with hubs_lock:
        if hubs.get(hub.key) is hub and not hub.clients:
            hubs.pop(hub.key, None)
            task_to_cancel = hub.upstream_task

    # Remove the empty hub while holding the lock before cancellation. A new
    # browser will therefore create a fresh hub instead of racing onto a task
    # that is in the process of being cancelled.
    if task_to_cancel and not task_to_cancel.done():
        task_to_cancel.cancel()


async def client_handler(client: ServerConnection) -> None:
    hub: Optional[Hub] = None

    try:
        first_message = await asyncio.wait_for(client.recv(), timeout=15)
        join = parse_client_join(client, first_message)

        async with hubs_lock:
            hub = hubs.get(join.key)

            if hub is None:
                hub = Hub(
                    key=join.key,
                    upstream_uri=join.upstream_uri,
                    upstream_origin=join.upstream_origin,
                    canonical_join=first_message,
                    upstream_host=join.upstream_host,
                    token_id=join.token_id,
                    channel=join.channel,
                    renderer_mode=join.renderer_mode,
                )
                hubs[join.key] = hub

            hub.clients.add(client)
            hub.join_trx[client] = join.trx_id

            if hub.leader is None:
                hub.leader = client

            if hub.upstream_task is None or hub.upstream_task.done():
                hub.upstream_task = asyncio.create_task(upstream_loop(hub))

        log.info("client connected hub=%s clients=%d", hub.key, len(hub.clients))
        await send_join_response(hub, client)

        async for message in client:
            payload = parse_json(message)

            if payload is None:
                if client is hub.leader:
                    await send_upstream(hub, message)
                continue

            action = payload.get("action")

            if action == "join":
                new_trx_id = str(payload.get("trxId") or "")
                if new_trx_id:
                    hub.join_trx[client] = new_trx_id
                    hub.acknowledged.discard(client)
                    await send_join_response(hub, client)
                continue

            if action == "call_res":
                trx_id = str(payload.get("trxId") or "")
                if not trx_id or first_call_response(hub, trx_id):
                    await send_upstream(hub, message)
                continue

            if client is hub.leader:
                await send_upstream(hub, message)

    except asyncio.TimeoutError:
        await client.close(code=1008, reason="join timeout")
    except ValueError as exc:
        log.warning("client rejected: %s", exc)
        await client.close(code=1008, reason=str(exc)[:120])
    except ConnectionClosed:
        pass
    except Exception:
        log.exception("client handler failed")
    finally:
        if hub is not None:
            await remove_client(hub, client)


async def main() -> None:
    log.info("listening on ws://%s:%d", LISTEN_HOST, LISTEN_PORT)
    status_task = asyncio.create_task(status_loop())
    try:
        async with serve(
            client_handler,
            LISTEN_HOST,
            LISTEN_PORT,
            compression="deflate",
            ping_interval=20,
            ping_timeout=20,
            close_timeout=5,
            max_size=None,
            max_queue=64,
            server_header=None,
        ):
            await asyncio.Future()
    finally:
        status_task.cancel()
        try:
            await status_task
        except asyncio.CancelledError:
            pass


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        pass
