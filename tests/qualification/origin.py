"""Loopback-bound synthetic HTTPS, CDN and WebSocket fixture."""
import asyncio
import json
import ssl
import sys
from http import HTTPStatus
from urllib.parse import urlsplit, parse_qs

from websockets.asyncio.server import serve
from websockets.http11 import Response
from websockets.datastructures import Headers

active = {}
count = 0


def http(_ws, request):
    global count
    path = urlsplit(request.path).path
    if path == "/ws":
        return None
    count += 1
    if path == "/fixture-status":
        body = json.dumps({"active": len(active), "connections": sum(active.values())}).encode()
    else:
        body = f"qualified HTTPS sample {count}".encode()
        if path == "/render":
            body += b'<link href="https://webcodercdn.atum.live/templates/qualification.woff2">'
    content_type = "text/html" if path == "/render" else "application/octet-stream"
    return Response(HTTPStatus.OK, "OK", Headers([
        ("Content-Type", content_type), ("Content-Length", str(len(body))),
        ("Connection", "close"),
    ]), body)


async def upstream(ws):
    token = parse_qs(urlsplit(ws.request.path).query)["token"][0]
    active[token] = active.get(token, 0) + 1
    try:
        join = json.loads(await ws.recv())
        assert join["action"] == "join"
        await ws.send(json.dumps({"action": "join_res", "trxId": join["trxId"]}))
        await asyncio.sleep(0.03)
        for i in range(1, 6):
            await ws.send(i.to_bytes(4, "big") + b"qualification-frame")
            await asyncio.sleep(0.08)
        await ws.wait_closed()
    finally:
        active[token] -= 1
        if not active[token]:
            del active[token]


async def main():
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(sys.argv[1], sys.argv[2])
    async with serve(upstream, "93.184.215.254", 443, ssl=ctx, process_request=http, compression=None):
        await asyncio.Future()


asyncio.run(main())
