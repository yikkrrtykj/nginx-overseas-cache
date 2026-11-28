"""Application probes and monotonic service interruption sampling."""
import argparse
import asyncio
import csv
import json
import ssl
import time
import uuid
from urllib import request

from websockets.asyncio.client import connect

http = "http://10.84.72.1:23333"
ws_root = "ws://10.84.72.1:23333/ws"
uri = "https://webcoder.atum.live/fixture-status"


async def websocket(token, index):
    peer = await connect(f"{ws_root}?token={token}", proxy=None, open_timeout=0.8, close_timeout=0.15)
    try:
        await peer.send(json.dumps({"action": "join", "trxId": str(index),
                                   "data": {"channelId": "qualification", "rendererMode": "onair"}}))
        ack = json.loads(await asyncio.wait_for(peer.recv(), timeout=1))
        assert ack["action"] == "join_res" and ack["trxId"] == str(index)
        return peer
    except BaseException:
        await peer.close()
        raise


async def verify_group():
    token = "lab-ws-" + uuid.uuid4().hex
    async with await websocket(token, 1) as a, await websocket(token, 2) as b:
        with request.urlopen(uri, context=ssl.create_default_context(), timeout=1) as response:
            assert json.load(response) == {"active": 1, "connections": 1}
        for i in range(1, 6):
            expected = i.to_bytes(4, "big") + b"qualification-frame"
            for client in (a, b):
                assert await asyncio.wait_for(client.recv(), 1) == expected


async def sample_ws():
    async with await websocket("lab-timing-" + uuid.uuid4().hex, 1) as client:
        assert await asyncio.wait_for(client.recv(), 0.4) == b"\0\0\0\1qualification-frame"


def sample_http():
    with request.urlopen(http + "/render", timeout=0.6) as response:
        assert response.status == 200 and b"qualified HTTPS sample" in response.read()


async def sample(channel, output):
    method = sample_ws if channel == "ws" else None
    writer = csv.writer(output)
    writer.writerow(("monotonic_ns", "duration_ns", "outcome"))
    while True:
        start = time.monotonic_ns()
        try:
            if method:
                await method()
            else:
                sample_http()
            status = "ok"
        except Exception as exc:
            status = type(exc).__name__
        writer.writerow((start, time.monotonic_ns() - start, status))
        output.flush()
        await asyncio.sleep(0.18)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("channel", choices=("ws", "http", "verify"))
    parser.add_argument("output", nargs="?", type=argparse.FileType("w"))
    args = parser.parse_args()
    if args.channel == "verify":
        asyncio.run(verify_group())
    else:
        asyncio.run(sample(args.channel, args.output))
