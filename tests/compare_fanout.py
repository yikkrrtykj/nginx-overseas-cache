"""Compare separate V1/V2 processes through the same isolated TLS fixture."""
import argparse
import asyncio
import json
import math
import os
from pathlib import Path
import socket
import ssl
import sys
import tempfile
import time

from websockets.asyncio.client import connect
from websockets.asyncio.server import serve

HOST = "webcoder.atum.live"
PORTABLE_HOST = "origin-bench.example"
IP = "93.184.215.254"


def readings(pid):
    if sys.platform == "win32":
        return windows_readings(pid)
    ticks = int(os.sysconf("SC_CLK_TCK"))
    fields = Path(f"/proc/{pid}/stat").read_text().split(") ", 1)[1].split()
    rss = next(
        int(line.split()[1])
        for line in Path(f"/proc/{pid}/status").read_text().splitlines()
        if line.startswith("VmRSS:")
    )
    return (int(fields[11]) + int(fields[12])) / ticks, rss


def windows_readings(pid):
    import ctypes
    from ctypes import wintypes

    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    ps = ctypes.WinDLL("psapi", use_last_error=True)
    kernel.OpenProcess.argtypes = (wintypes.DWORD, wintypes.BOOL, wintypes.DWORD)
    kernel.OpenProcess.restype = wintypes.HANDLE
    kernel.GetProcessTimes.argtypes = (wintypes.HANDLE,) + (ctypes.POINTER(wintypes.FILETIME),) * 4
    kernel.CloseHandle.argtypes = (wintypes.HANDLE,)

    class Counters(ctypes.Structure):
        _fields_ = [("cb", wintypes.DWORD), ("faults", wintypes.DWORD)] + [
            (name, ctypes.c_size_t) for name in (
                "peak_ws", "ws", "peak_paged", "paged", "peak_nonpaged", "nonpaged",
                "pagefile", "peak_pagefile",
            )
        ]

    ps.GetProcessMemoryInfo.argtypes = (wintypes.HANDLE, ctypes.POINTER(Counters), wintypes.DWORD)
    handle = kernel.OpenProcess(0x0410, False, pid)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        times = [wintypes.FILETIME() for _ in range(4)]
        if not kernel.GetProcessTimes(handle, *(ctypes.byref(value) for value in times)):
            raise ctypes.WinError(ctypes.get_last_error())
        info = Counters()
        info.cb = ctypes.sizeof(info)
        if not ps.GetProcessMemoryInfo(handle, ctypes.byref(info), info.cb):
            raise ctypes.WinError(ctypes.get_last_error())
        consumed = sum((value.dwHighDateTime << 32) | value.dwLowDateTime for value in times[2:])
        return consumed / 10_000_000, info.ws // 1024
    finally:
        kernel.CloseHandle(handle)


async def until(predicate, seconds=10):
    until_time = time.monotonic() + seconds
    while time.monotonic() < until_time:
        if predicate():
            return
        await asyncio.sleep(.02)
    raise RuntimeError("benchmark fixture timed out")


async def run_case(command, name, count, group_count, size, messages, cert, portable):
    origin = {}
    opened = 0

    async def upstream(ws):
        nonlocal opened
        msg = json.loads(await ws.recv())
        token = ws.request.path.split("token=", 1)[1]
        assert token not in origin, "duplicate active overseas connection"
        origin[token] = ws
        opened += 1
        try:
            await ws.send(json.dumps({"action": "join_res", "trxId": msg["trxId"]}))
            await ws.wait_closed()
        finally:
            origin.pop(token, None)

    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    endpoint = ("127.0.0.1", 0) if portable else (IP, 443)
    host = PORTABLE_HOST if portable else HOST
    context = run_case.context
    async with serve(upstream, *endpoint, ssl=context, compression=None) as simulated:
        peer = simulated.sockets[0].getsockname()
        runner_environment = {}
        if portable:
            runner_environment = {"OVERSEAS_BENCH_LOOPBACK": f"127.0.0.1:{peer[1]}",
                                  "OVERSEAS_BENCH_HOSTNAME": host}
        return await run_relay(command, name, count, group_count, size, messages, cert,
                               port, host, runner_environment, origin, lambda: opened)


async def run_relay(command, name, count, group_count, size, messages, cert, port,
                    host, runner_environment, origin, opened):
    with tempfile.TemporaryDirectory(prefix="fanout-bench-") as tmp:
        status_file = Path(tmp) / "status.json"
        process = await asyncio.create_subprocess_exec(
            *command,
            env=dict(os.environ, LISTEN_HOST="127.0.0.1", LISTEN_PORT=str(port), SSL_CERT_FILE=cert,
                     STATUS_FILE=str(status_file), STATUS_INTERVAL="1", LOG_LEVEL="WARNING", **runner_environment),
            stdout=asyncio.subprocess.DEVNULL, stderr=asyncio.subprocess.DEVNULL,
        )
        clients = []
        try:
            await until(status_file.exists)
            before = readings(process.pid)
            for index in range(count):
                token = f"case-token-{index % group_count}"
                ws = await connect(
                    f"ws://127.0.0.1:{port}/ws?token={token}",
                    additional_headers={"X-Fanout-Upstream-Base": f"wss://{host}/ws",
                                        "X-Fanout-Upstream-Origin": f"https://{host}"},
                    compression=None, proxy=None,
                )
                clients.append(ws)
                await ws.send(json.dumps({"action": "join", "trxId": str(index + 1),
                                          "data": {"channelId": str(index % group_count), "rendererMode": "onair"}}))
                ack = json.loads(await asyncio.wait_for(ws.recv(), 8))
                assert ack["action"] == "join_res" and ack["trxId"] == str(index + 1)

            assert len(origin) == group_count and opened() == group_count
            def published():
                try:
                    data = json.loads(status_file.read_text())
                    return data["localClients"] == count and data["upstreamConnections"] == group_count
                except (OSError, ValueError, KeyError):
                    return False

            await until(published)
            joined = readings(process.pid)
            totals = []
            start = time.perf_counter()
            for sequence in range(messages):
                packet = sequence.to_bytes(4, "big") + b"z" * (size - 4)
                indices = [index for index in range(count) if index % group_count == sequence % group_count]
                sent = time.perf_counter()

                async def received(ws):
                    result = await asyncio.wait_for(ws.recv(), 8)
                    assert result == packet, "out-of-order or corrupt fanout delivery"
                    return (time.perf_counter() - sent) * 1000

                await origin[f"case-token-{sequence % group_count}"].send(packet)
                totals.extend(await asyncio.gather(*(received(clients[i]) for i in indices)))

            elapsed = time.perf_counter() - start
            steady = readings(process.pid)
            snapshot = json.loads(status_file.read_text())
            await asyncio.gather(*(ws.close() for ws in clients))
            await until(lambda: not origin)
            await asyncio.sleep(.15)
            after = readings(process.pid)
            totals.sort()
            return {
                "runtime": name,
                "clients": count,
                "groups": group_count,
                "payloadBytes": size,
                "upstreamConnectionAttempts": opened(),
                "deliveriesChecked": len(totals),
                "deliveriesPerSecond": round(len(totals) / elapsed, 1),
                "meanRelayCpuPct": round((steady[0] - joined[0]) / elapsed * 100, 1),
                "rssIdleKiB": before[1], "rssJoinedKiB": joined[1],
                "rssPostTrafficKiB": steady[1], "rssAfterDisconnectKiB": after[1],
                "goroutines": snapshot.get("goroutines"),
                **{f"p{q}DeliveryMs": round(totals[math.ceil(len(totals) * q / 100) - 1], 3) for q in (50, 95, 99)},
            }
        finally:
            if process.returncode is None:
                process.terminate()
                try:
                    await asyncio.wait_for(process.wait(), 10)
                except TimeoutError:
                    process.kill()
                    await process.wait()


async def main(args):
    if not args.portable:
        assert set(socket.gethostbyname_ex(HOST)[2]) == {IP}, "fixture DNS must resolve only to the test IP"
        with socket.socket() as check:
            check.bind((IP, 0))  # Fail before sending traffic if this is not a local address.
    v1 = [sys.executable, args.v1]
    v2 = [args.v2]
    if args.portable:
        v1.insert(1, str(Path(__file__).with_name("bench_v1_loopback.py")))
        v2.append("-test.run=^TestBenchmarkEndpoint$")
    run_case.context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    run_case.context.load_cert_chain(args.cert, args.key)
    results = []
    for clients in (1, 10, 30, 100, 300):
        for groups, payload in ((1, 1024), (min(5, clients), 16384)):
            for name, command in (("V1", v1), ("V2", v2)):
                result = await run_case(command, name, clients, groups, payload, args.messages, args.cert, args.portable)
                print(json.dumps(result), flush=True)
                results.append(result)
    Path(args.output).write_text(json.dumps(results, indent=2) + "\n")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--v1", required=True)
    parser.add_argument("--v2", required=True)
    parser.add_argument("--cert", required=True)
    parser.add_argument("--key", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--portable", action="store_true", help="use a Go test binary and a loopback TLS fixture")
    parser.add_argument("--messages", type=int, default=80)
    asyncio.run(main(parser.parse_args()))
