"""Run historical V1 with an injected loopback socket for the portable benchmark."""
import asyncio
import importlib.util
import ipaddress
import os
import sys

spec = importlib.util.spec_from_file_location("historical_fanout", sys.argv[1])
fanout = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = fanout
spec.loader.exec_module(fanout)
host, port = os.environ["OVERSEAS_BENCH_LOOPBACK"].rsplit(":", 1)
if not ipaddress.ip_address(host).is_loopback:
    raise SystemExit("benchmark origin must be local")
direct = fanout.connect


def connect(*args, **kwargs):
    return direct(*args, **kwargs, host=host, port=int(port))


fanout.connect = connect
asyncio.run(fanout.main())
