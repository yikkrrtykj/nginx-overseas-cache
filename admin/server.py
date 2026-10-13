#!/usr/bin/env python3
"""LAN-restricted administration UI for the Nginx overseas cache."""

from __future__ import annotations

import argparse
import fcntl
import hashlib
import hmac
import http.client
import ipaddress
import json
import os
import re
import secrets
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import zlib
from dataclasses import dataclass
from datetime import datetime, timezone
from http import HTTPStatus
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from html import unescape
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import parse_qs, urljoin, urlsplit, urlunsplit


UPSTREAM_CONFIG = Path("/etc/nginx/conf.d/proxy-cache.conf")
SITE_CONFIG = Path("/etc/nginx/sites-available/overseas-cache")
SETTINGS_CONFIG = Path("/etc/nginx/overseas-cache/settings.conf")
PASSWORD_CONFIG = Path("/etc/nginx/overseas-cache/admin-password.json")
BACKUP_ROOT = Path("/etc/nginx/overseas-cache/backups")
LOCK_FILE = Path("/run/overseas-cache-admin.lock")
FANOUT_STATUS = Path("/run/ws-fanout/status.json")
STATIC_ROOT = Path(__file__).resolve().parent / "static"

HOST_PATTERN = re.compile(
    r"(?=.{1,253}\Z)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+"
    r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\Z"
)
ID_PATTERN = re.compile(r"[A-Za-z0-9._~-]{1,128}\Z")
TOKEN_PATTERN = re.compile(r"[A-Za-z0-9._~=-]{8,512}\Z")
USERNAME_PATTERN = re.compile(r"[A-Za-z0-9._-]{1,64}\Z")
SETTING_PATTERN = re.compile(
    r'^\s*set\s+\$(render_channel|render_account|render_token)\s+"([^"]*)"\s*;\s*$',
    re.MULTILINE,
)

DEFAULT_ALLOWED_NETWORKS = (
    "192.168.0.0/16",
    "10.0.0.0/8",
    "172.16.0.0/12",
    "127.0.0.0/8",
)
PASSWORD_ITERATIONS = 600_000
SESSION_COOKIE = "overseas_admin_session"
SESSION_TTL = 8 * 60 * 60
PASSWORD_LOCK = threading.Lock()
DISCOVERY_LIMIT = threading.BoundedSemaphore(2)
DISCOVERY_MAX_DOCUMENT = 2 * 1024 * 1024
DISCOVERY_MAX_ASSETS = 10
DISCOVERY_MAX_TOTAL = 6 * 1024 * 1024
DISCOVERY_URL_PATTERN = re.compile(r"(?:(?:https?:)?//)[^\s\"'<>\\)]+", re.IGNORECASE)
RESOURCE_EXTENSIONS = {
    ".css": "样式",
    ".js": "脚本",
    ".json": "数据",
    ".otf": "字体",
    ".ttf": "字体",
    ".woff": "字体",
    ".woff2": "字体",
    ".png": "图片",
    ".jpg": "图片",
    ".jpeg": "图片",
    ".gif": "图片",
    ".webp": "图片",
    ".svg": "图片",
    ".ico": "图片",
    ".mp4": "媒体",
    ".webm": "媒体",
    ".m3u8": "媒体",
}


class AdminError(RuntimeError):
    pass


@dataclass(frozen=True)
class CurrentConfig:
    origin_host: str
    cdn_host: str
    channel_id: str
    account_id: str
    token: str


@dataclass(frozen=True)
class DiscoveryFetch:
    url: str
    status: int
    content_type: str
    body: bytes


class ResourceHTMLParser(HTMLParser):
    RESOURCE_ATTRIBUTES = {
        "script": ("src",),
        "link": ("href",),
        "img": ("src", "srcset", "data-src"),
        "source": ("src", "srcset"),
        "video": ("src", "poster"),
        "audio": ("src",),
        "iframe": ("src",),
        "object": ("data",),
    }

    def __init__(self, base_url: str) -> None:
        super().__init__(convert_charrefs=True)
        self.base_url = base_url
        self.urls: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        values = {name.lower(): value for name, value in attrs if value}
        if tag.lower() == "base" and values.get("href"):
            self.base_url = urljoin(self.base_url, values["href"])
        for name in self.RESOURCE_ATTRIBUTES.get(tag.lower(), ()):
            value = values.get(name)
            if not value:
                continue
            if name == "srcset":
                for candidate in value.split(","):
                    self.urls.append(urljoin(self.base_url, candidate.strip().split()[0]))
            else:
                self.urls.append(urljoin(self.base_url, value.strip()))


def normalize_discovery_url(value: object, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise AdminError(f"请填写{label}")
    text = unescape(value.strip())
    match = re.search(r"https://[^\s\"'<>]+", text, re.IGNORECASE)
    if not match:
        raise AdminError(f"{label}必须是完整的 https:// 地址")
    candidate = match.group(0).rstrip(".,;)]}")
    try:
        parsed = urlsplit(candidate)
        hostname = parsed.hostname
        port = parsed.port
    except ValueError as exc:
        raise AdminError(f"{label}格式不正确") from exc
    if (
        parsed.scheme.lower() != "https"
        or not hostname
        or parsed.username is not None
        or parsed.password is not None
        or port not in (None, 443)
    ):
        raise AdminError(f"{label}只允许标准 HTTPS 公网地址")
    try:
        ascii_host = hostname.encode("idna").decode("ascii").lower().rstrip(".")
    except UnicodeError as exc:
        raise AdminError(f"{label}域名格式不正确") from exc
    try:
        ipaddress.ip_address(ascii_host)
    except ValueError:
        pass
    else:
        raise AdminError(f"{label}必须使用公网域名，不能直接填写 IP 地址")
    if not HOST_PATTERN.fullmatch(ascii_host):
        raise AdminError(f"{label}必须使用有效的公网域名")
    path = parsed.path or "/"
    return urlunsplit(("https", ascii_host, path, parsed.query, ""))


def resolve_public_addresses(hostname: str, port: int = 443) -> tuple[str, ...]:
    try:
        answers = socket.getaddrinfo(hostname, port, type=socket.SOCK_STREAM)
    except OSError as exc:
        raise AdminError(f"无法解析域名：{hostname}") from exc
    addresses: list[str] = []
    for answer in answers:
        value = answer[4][0].split("%", 1)[0]
        if value not in addresses:
            addresses.append(value)
    if not addresses:
        raise AdminError(f"域名没有可用地址：{hostname}")
    for value in addresses:
        try:
            address = ipaddress.ip_address(value)
        except ValueError as exc:
            raise AdminError(f"域名返回了无效地址：{hostname}") from exc
        if not address.is_global:
            raise AdminError(f"为保护内网，禁止扫描解析到私有或保留地址的域名：{hostname}")
    addresses.sort(key=lambda value: 0 if ipaddress.ip_address(value).version == 4 else 1)
    return tuple(addresses)


def decode_discovery_body(body: bytes, content_encoding: str, maximum: int) -> bytes:
    encoding = content_encoding.lower().strip()
    try:
        if encoding == "gzip":
            return zlib.decompressobj(16 + zlib.MAX_WBITS).decompress(body, maximum)
        if encoding == "deflate":
            return zlib.decompressobj().decompress(body, maximum)
    except zlib.error:
        return b""
    return body[:maximum]


def fetch_public_url(url: str, maximum: int = DISCOVERY_MAX_DOCUMENT) -> DiscoveryFetch:
    current = normalize_discovery_url(url, "扫描地址")
    for _redirect in range(5):
        parsed = urlsplit(current)
        hostname = parsed.hostname or ""
        path = urlunsplit(("", "", parsed.path or "/", parsed.query, ""))
        last_error: Exception | None = None
        for address in resolve_public_addresses(hostname):
            connection = http.client.HTTPConnection(hostname, 443, timeout=10)
            raw_socket: socket.socket | None = None
            try:
                raw_socket = socket.create_connection((address, 443), timeout=8)
                connection.sock = ssl.create_default_context().wrap_socket(
                    raw_socket, server_hostname=hostname
                )
                raw_socket = None
                connection.request(
                    "GET",
                    path,
                    headers={
                        "Host": hostname,
                        "User-Agent": "OverseasCacheDiscovery/1.0",
                        "Accept": "text/html,text/css,application/javascript,application/json,*/*;q=0.5",
                        "Accept-Encoding": "identity",
                        "Range": f"bytes=0-{maximum - 1}",
                        "Connection": "close",
                    },
                )
                response = connection.getresponse()
                body = response.read(maximum + 1)
                status = response.status
                content_type = response.getheader("Content-Type", "").split(";", 1)[0].lower()
                content_encoding = response.getheader("Content-Encoding", "")
                location = response.getheader("Location", "")
                body = decode_discovery_body(body, content_encoding, maximum)
                connection.close()
                break
            except (OSError, ssl.SSLError, http.client.HTTPException) as exc:
                last_error = exc
                if raw_socket is not None:
                    raw_socket.close()
                connection.close()
        else:
            raise AdminError(f"服务器无法连接到：{hostname}") from last_error
        if status in {301, 302, 303, 307, 308}:
            if not location:
                raise AdminError(f"上游返回了无目标地址的跳转：{hostname}")
            current = normalize_discovery_url(urljoin(current, location), "跳转地址")
            continue
        return DiscoveryFetch(current, status, content_type, body)
    raise AdminError("网页跳转次数过多，已停止扫描")


def classify_resource(url: str, content_type: str = "") -> tuple[str, int]:
    mime = content_type.lower()
    path = urlsplit(url).path.lower()
    if "font" in mime or any(path.endswith(extension) for extension in (".otf", ".ttf", ".woff", ".woff2")):
        return "字体", 12
    if "/templates/" in path:
        return "模板资源", 10
    if "text/css" in mime or path.endswith(".css"):
        return "样式", 7
    if mime.startswith("image/") or any(path.endswith(extension) for extension in (".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico")):
        return "图片", 5
    if mime.startswith("video/") or mime.startswith("audio/") or any(path.endswith(extension) for extension in (".mp4", ".webm", ".m3u8")):
        return "媒体", 5
    if "javascript" in mime or path.endswith(".js"):
        return "脚本", 3
    if "json" in mime or path.endswith(".json"):
        return "数据", 2
    if "text/html" in mime or path.endswith((".html", ".htm")):
        return "页面", 1
    extension = Path(path).suffix
    if extension in RESOURCE_EXTENSIONS:
        return RESOURCE_EXTENSIONS[extension], 1
    return "", 0


def text_resource(content_type: str, url: str) -> bool:
    mime = content_type.lower()
    path = urlsplit(url).path.lower()
    return (
        mime.startswith("text/")
        or "javascript" in mime
        or "json" in mime
        or path.endswith((".html", ".htm", ".css", ".js", ".json"))
    )


def extract_resource_urls(text: str, base_url: str, content_type: str) -> list[str]:
    normalized_text = text.replace("\\/", "/").replace("\\u002F", "/").replace("\\u002f", "/")
    raw_urls: list[str] = []
    if "html" in content_type or urlsplit(base_url).path.lower().endswith((".html", ".htm", "/")):
        parser = ResourceHTMLParser(base_url)
        try:
            parser.feed(normalized_text)
            raw_urls.extend(parser.urls)
        except Exception:
            pass
    raw_urls.extend(match.group(0) for match in DISCOVERY_URL_PATTERN.finditer(normalized_text))
    relative_pattern = re.compile(
        r"[\"'(]((?:/|\./|\.\./)[^\s\"'()<>]+\.(?:html?|css|js|json|otf|ttf|woff2?|png|jpe?g|gif|webp|svg|ico|mp4|webm|m3u8)(?:\?[^\s\"'()<>]*)?)",
        re.IGNORECASE,
    )
    raw_urls.extend(match.group(1) for match in relative_pattern.finditer(normalized_text))

    results: list[str] = []
    for raw_url in raw_urls:
        if raw_url.startswith("//"):
            raw_url = "https:" + raw_url
        candidate = urljoin(base_url, raw_url)
        try:
            candidate = normalize_discovery_url(candidate, "资源地址")
        except AdminError:
            continue
        if candidate not in results:
            results.append(candidate)
    return results


def parse_render_parameters(url: str) -> dict[str, str]:
    values = {key.lower(): entries[-1] for key, entries in parse_qs(urlsplit(url).query).items() if entries}
    result: dict[str, str] = {}
    mappings = (("channelid", "channelId"), ("accountid", "accountId"), ("token", "token"))
    for source_key, result_key in mappings:
        value = values.get(source_key, "")
        if not value:
            continue
        try:
            if result_key == "token":
                result[result_key] = validate_token(value, "")
            else:
                result[result_key] = validate_id(value, result_key)
        except AdminError:
            continue
    return result


def safe_sample_path(url: str) -> str:
    parsed = urlsplit(url)
    path = parsed.path or "/"
    path = re.sub(
        r"^(/templates-auth/[^/]+/)[^/]+",
        r"\1<token>",
        path,
        flags=re.IGNORECASE,
    )
    return path if len(path) <= 160 else path[:157] + "..."


def related_site_host(origin_host: str, candidate_host: str) -> bool:
    origin_parts = origin_host.lower().split(".")
    candidate_parts = candidate_host.lower().split(".")
    return candidate_host == origin_host or (
        len(origin_parts) >= 2
        and len(candidate_parts) >= 2
        and origin_parts[-2:] == candidate_parts[-2:]
    )


def discover_site(source_value: object, evidence_value: object = "") -> dict[str, object]:
    source_url = normalize_discovery_url(source_value, "原始网页地址")
    evidence_url = ""
    if isinstance(evidence_value, str) and evidence_value.strip():
        evidence_url = normalize_discovery_url(evidence_value, "F12 资源地址")
    elif evidence_value not in (None, ""):
        raise AdminError("F12 资源地址格式不正确")

    if not DISCOVERY_LIMIT.acquire(timeout=1):
        raise AdminError("当前正在执行其他识别任务，请稍后重试")
    try:
        source_fetch = fetch_public_url(source_url)
        if source_fetch.status < 200 or source_fetch.status >= 400:
            raise AdminError(f"原始网页返回 HTTP {source_fetch.status}，无法继续识别")
        origin_host = urlsplit(source_fetch.url).hostname or ""
        parameters = parse_render_parameters(source_url)
        destination_parameters = parse_render_parameters(source_fetch.url)
        identity_changed = any(
            key in parameters
            and key in destination_parameters
            and parameters[key] != destination_parameters[key]
            for key in ("channelId", "accountId")
        )
        if identity_changed:
            # A new channel/account must not inherit credentials from the old URL.
            parameters = destination_parameters
        else:
            parameters.update(destination_parameters)
        candidates: dict[str, dict[str, object]] = {}
        queue: list[str] = []
        queued: set[str] = {source_fetch.url}
        inspected = 1
        downloaded = len(source_fetch.body)
        warnings: list[str] = []

        def record(url: str, content_type: str = "", verified: bool = False, status: int = 0) -> None:
            host = urlsplit(url).hostname or ""
            category, weight = classify_resource(url, content_type)
            if not host or not category:
                return
            item = candidates.setdefault(
                host,
                {
                    "host": host,
                    "score": 0,
                    "types": set(),
                    "samples": [],
                    "firstUrl": url,
                    "compatible": False,
                    "verified": False,
                    "valid": False,
                    "status": 0,
                    "contentType": "",
                },
            )
            item["score"] = int(item["score"]) + weight
            item["types"].add(category)  # type: ignore[union-attr]
            sample = safe_sample_path(url)
            if urlsplit(url).path.lower().startswith("/templates/"):
                item["compatible"] = True
            if sample not in item["samples"] and len(item["samples"]) < 2:  # type: ignore[arg-type]
                item["samples"].append(sample)  # type: ignore[union-attr]
            if verified:
                valid_response = 200 <= status < 300 and not (
                    category != "页面" and content_type == "text/html"
                )
                item["verified"] = True
                item["valid"] = valid_response
                item["status"] = status
                item["contentType"] = content_type or "未知类型"
                item["score"] = int(item["score"]) + (15 if valid_response else -30)

        def scan_text(fetch: DiscoveryFetch) -> None:
            if not text_resource(fetch.content_type, fetch.url) or not fetch.body:
                return
            text = fetch.body.decode("utf-8", errors="replace")
            for resource_url in extract_resource_urls(text, fetch.url, fetch.content_type):
                record(resource_url)
                resource_host = urlsplit(resource_url).hostname or ""
                _category, weight = classify_resource(resource_url)
                if (
                    weight > 0
                    and resource_url not in queued
                    and related_site_host(origin_host, resource_host)
                    and len(queue) < DISCOVERY_MAX_ASSETS * 3
                ):
                    queue.append(resource_url)
                    queued.add(resource_url)

        scan_text(source_fetch)
        while queue and inspected < DISCOVERY_MAX_ASSETS and downloaded < DISCOVERY_MAX_TOTAL:
            resource_url = queue.pop(0)
            category, _weight = classify_resource(resource_url)
            if category not in {"页面", "样式", "脚本", "数据"}:
                continue
            try:
                fetched = fetch_public_url(resource_url, min(DISCOVERY_MAX_DOCUMENT, DISCOVERY_MAX_TOTAL - downloaded))
            except AdminError:
                continue
            inspected += 1
            downloaded += len(fetched.body)
            record(fetched.url, fetched.content_type, True, fetched.status)
            if 200 <= fetched.status < 300:
                scan_text(fetched)

        verification_items = sorted(
            (
                item
                for host, item in candidates.items()
                if host != origin_host and not item["verified"]
            ),
            key=lambda item: int(item["score"]),
            reverse=True,
        )
        for item in verification_items[:3]:
            first_url = str(item["firstUrl"])
            first_host = urlsplit(first_url).hostname or ""
            if not related_site_host(origin_host, first_host):
                continue
            try:
                fetched = fetch_public_url(first_url, 128 * 1024)
            except AdminError:
                continue
            inspected += 1
            record(fetched.url, fetched.content_type, True, fetched.status)

        if evidence_url:
            evidence_fetch = fetch_public_url(evidence_url, 128 * 1024)
            inspected += 1
            record(evidence_fetch.url, evidence_fetch.content_type, True, evidence_fetch.status)
            if not 200 <= evidence_fetch.status < 300:
                warnings.append(f"F12 资源返回 HTTP {evidence_fetch.status}，不建议直接使用该域名")
            elif evidence_fetch.content_type == "text/html":
                warnings.append("F12 资源返回的是 HTML，不像字体、图片或脚本，请换一个成功的资源请求")

        ranked: list[dict[str, object]] = []
        for host, item in candidates.items():
            if host == origin_host:
                item["score"] = int(item["score"]) - 4
            if evidence_url and host == (urlsplit(evidence_url).hostname or ""):
                item["score"] = int(item["score"]) + 30
            if int(item["score"]) <= 0:
                continue
            ranked.append(
                {
                    "host": host,
                    "score": int(item["score"]),
                    "types": sorted(item["types"]),
                    "samples": list(item["samples"]),
                    "compatible": bool(item["compatible"]),
                    "verified": bool(item["verified"]),
                    "valid": bool(item["valid"]),
                    "status": int(item["status"]),
                    "contentType": str(item["contentType"]),
                }
            )
        ranked.sort(
            key=lambda item: (
                bool(item["compatible"]),
                bool(item["valid"]),
                bool(item["verified"]),
                int(item["score"]),
            ),
            reverse=True,
        )
        cdn_candidates = [item for item in ranked if item["host"] != origin_host]
        compatible_candidates = [item for item in cdn_candidates if item["compatible"]]
        evidence_host = urlsplit(evidence_url).hostname if evidence_url else ""
        evidence_candidates = [
            item
            for item in cdn_candidates
            if item["host"] == evidence_host and item["valid"] and item["compatible"]
        ]
        verified_compatible = [item for item in compatible_candidates if item["valid"]]
        recommended = str(
            (evidence_candidates or verified_compatible or compatible_candidates or [{"host": ""}])[0]["host"]
        )
        missing_parameters = [
            key for key in ("channelId", "accountId", "token") if not parameters.get(key)
        ]
        if missing_parameters:
            warnings.append(
                f"未识别出 {'、'.join(missing_parameters)}；请填写完整的新网页地址，"
                "或在配置中补全这些字段后保存，避免沿用旧频道参数"
            )
        if not cdn_candidates:
            warnings.append("自动扫描没有确认独立 CDN；可以粘贴 F12 中任意成功字体、图片或 JS 的请求地址再次识别")
        elif not compatible_candidates:
            warnings.append("候选资源不在当前代理支持的 /templates/ 路径下；域名可供参考，但可能还需要适配 Nginx 路径规则")
        return {
            "sourceHost": origin_host,
            "sourcePath": safe_sample_path(source_fetch.url),
            "channelId": parameters.get("channelId", ""),
            "accountId": parameters.get("accountId", ""),
            "token": parameters.get("token", ""),
            "recommendedHost": recommended,
            "candidates": ranked[:8],
            "inspectedCount": inspected,
            "warnings": warnings,
        }
    finally:
        DISCOVERY_LIMIT.release()


def parse_allowed_networks(value: str | None) -> tuple[ipaddress.IPv4Network | ipaddress.IPv6Network, ...]:
    entries = DEFAULT_ALLOWED_NETWORKS if value is None else value.split(",")
    networks = []
    for entry in entries:
        normalized = entry.strip()
        if not normalized:
            continue
        try:
            networks.append(ipaddress.ip_network(normalized, strict=False))
        except ValueError as exc:
            raise AdminError(f"无效的管理网段：{normalized}") from exc
    if not networks:
        raise AdminError("至少需要配置一个管理网段")
    return tuple(networks)


def address_is_allowed(
    value: str,
    networks: tuple[ipaddress.IPv4Network | ipaddress.IPv6Network, ...],
) -> bool:
    try:
        address = ipaddress.ip_address(value.split("%", 1)[0])
    except ValueError:
        return False
    if isinstance(address, ipaddress.IPv6Address) and address.ipv4_mapped:
        address = address.ipv4_mapped
    return any(address.version == network.version and address in network for network in networks)


def host_is_allowed(
    host_header: str,
    networks: tuple[ipaddress.IPv4Network | ipaddress.IPv6Network, ...],
) -> bool:
    try:
        parsed = urlsplit(f"//{host_header}")
        hostname = parsed.hostname
    except ValueError:
        return False
    if (
        not hostname
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path
        or parsed.query
        or parsed.fragment
    ):
        return False
    if hostname.lower() == "localhost":
        return address_is_allowed("127.0.0.1", networks)
    return address_is_allowed(hostname, networks)


def same_origin_is_allowed(
    origin: str,
    host_header: str,
    networks: tuple[ipaddress.IPv4Network | ipaddress.IPv6Network, ...],
) -> bool:
    if not origin:
        return False
    try:
        parsed_origin = urlsplit(origin)
        parsed_host = urlsplit(f"//{host_header}")
        origin_port = parsed_origin.port or 80
        host_port = parsed_host.port or 80
    except ValueError:
        return False
    return (
        parsed_origin.scheme == "http"
        and parsed_origin.username is None
        and parsed_origin.password is None
        and parsed_origin.path in ("", "/")
        and not parsed_origin.query
        and not parsed_origin.fragment
        and parsed_origin.hostname == parsed_host.hostname
        and origin_port == host_port
        and host_is_allowed(host_header, networks)
    )


def run_checked(command: list[str], timeout: int) -> str:
    try:
        result = subprocess.run(
            command,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
            env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin"},
        )
    except subprocess.TimeoutExpired as exc:
        raise AdminError(f"命令执行超时：{' '.join(command)}") from exc

    output = "\n".join(part.strip() for part in (result.stdout, result.stderr) if part.strip())
    if result.returncode != 0:
        raise AdminError(output or f"命令执行失败：{' '.join(command)}")
    return output


def read_text(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError as exc:
        raise AdminError(f"无法读取 {path}") from exc


def parse_upstream_host(text: str, group: str) -> str:
    pattern = re.compile(
        rf"upstream\s+{re.escape(group)}\s*\{{.*?\bserver\s+([^\s:;]+):443\s*;",
        re.DOTALL,
    )
    match = pattern.search(text)
    if not match:
        raise AdminError(f"无法从 Nginx 配置识别 {group}")
    return match.group(1).lower()


def parse_settings(text: str) -> tuple[str, str, str]:
    values = {name: value for name, value in SETTING_PATTERN.findall(text)}
    missing = [name for name in ("render_channel", "render_account", "render_token") if not values.get(name)]
    if missing:
        raise AdminError("settings.conf 缺少必要参数：" + ", ".join(missing))
    return values["render_channel"], values["render_account"], values["render_token"]


def load_config() -> CurrentConfig:
    upstream_text = read_text(UPSTREAM_CONFIG)
    channel, account, token = parse_settings(read_text(SETTINGS_CONFIG))
    return CurrentConfig(
        origin_host=parse_upstream_host(upstream_text, "webcoder_origin"),
        cdn_host=parse_upstream_host(upstream_text, "webcoder_cdn"),
        channel_id=channel,
        account_id=account,
        token=token,
    )


def unavailable_fanout_status(message: str) -> dict[str, object]:
    return {
        "available": False,
        "message": message,
        "generatedAt": "",
        "uptimeSeconds": 0,
        "localClients": 0,
        "acknowledgedClients": 0,
        "activeHubs": 0,
        "upstreamConnections": 0,
        "savedUpstreamConnections": 0,
        "groups": [],
    }


def read_fanout_status(
    path: Path | None = None,
    *,
    now: float | None = None,
    maximum_age: float = 10,
) -> dict[str, object]:
    selected_path = FANOUT_STATUS if path is None else path
    current_time = time.time() if now is None else now
    descriptor: int | None = None
    try:
        flags = (
            os.O_RDONLY
            | getattr(os, "O_CLOEXEC", 0)
            | getattr(os, "O_NOFOLLOW", 0)
        )
        descriptor = os.open(selected_path, flags)
        file_stat = os.fstat(descriptor)
        if file_stat.st_size > 256 * 1024:
            raise ValueError("status file is too large")
        with os.fdopen(descriptor, encoding="utf-8") as stream:
            descriptor = None
            payload = json.load(stream)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, ValueError):
        return unavailable_fanout_status("fanout 暂未上报状态")
    finally:
        if descriptor is not None:
            os.close(descriptor)

    if not isinstance(payload, dict):
        return unavailable_fanout_status("fanout 状态格式无效")

    def safe_count(value: object) -> int:
        if isinstance(value, bool) or not isinstance(value, int) or value < 0:
            return 0
        return min(value, 1_000_000)

    def safe_text(value: object, maximum: int = 253) -> str:
        return value[:maximum] if isinstance(value, str) else ""

    groups: list[dict[str, object]] = []
    raw_groups = payload.get("groups", [])
    if isinstance(raw_groups, list):
        for item in raw_groups[:128]:
            if not isinstance(item, dict):
                continue
            groups.append(
                {
                    "origin": safe_text(item.get("origin")),
                    "tokenId": safe_text(item.get("tokenId"), 24),
                    "channel": safe_text(item.get("channel"), 128),
                    "mode": safe_text(item.get("mode"), 64),
                    "localClients": safe_count(item.get("localClients")),
                    "acknowledgedClients": safe_count(
                        item.get("acknowledgedClients")
                    ),
                    "upstreamConnected": item.get("upstreamConnected") is True,
                }
            )

    age = max(0.0, current_time - file_stat.st_mtime)
    available = age <= maximum_age
    return {
        "available": available,
        "message": "实时状态正常" if available else "fanout 状态已超过 10 秒未更新",
        "generatedAt": safe_text(payload.get("generatedAt"), 64),
        "uptimeSeconds": safe_count(payload.get("uptimeSeconds")),
        "localClients": safe_count(payload.get("localClients")),
        "acknowledgedClients": safe_count(payload.get("acknowledgedClients")),
        "activeHubs": safe_count(payload.get("activeHubs")),
        "upstreamConnections": safe_count(payload.get("upstreamConnections")),
        "savedUpstreamConnections": safe_count(
            payload.get("savedUpstreamConnections")
        ),
        "groups": groups,
    }


def validate_hostname(value: object, label: str) -> str:
    if not isinstance(value, str):
        raise AdminError(f"{label}格式不正确")
    host = value.strip().lower().rstrip(".")
    if not HOST_PATTERN.fullmatch(host):
        raise AdminError(f"{label}只填写域名，不要包含 https://、端口、路径或结尾斜杠")
    return host


def validate_id(value: object, label: str) -> str:
    if not isinstance(value, str):
        raise AdminError(f"{label}格式不正确")
    normalized = value.strip()
    if not ID_PATTERN.fullmatch(normalized):
        raise AdminError(f"{label}只能包含字母、数字、点、下划线、波浪号和连字符")
    return normalized


def validate_token(value: object, current_token: str) -> str:
    if value is None or value == "":
        return current_token
    if not isinstance(value, str) or not TOKEN_PATTERN.fullmatch(value.strip()):
        raise AdminError("token 格式不正确；为避免 URL 注入，只允许 URL 安全字符")
    return value.strip()


def replace_exact(text: str, pattern: re.Pattern[str], replacement: str, label: str) -> str:
    updated, count = pattern.subn(replacement, text, count=1)
    if count != 1:
        raise AdminError(f"无法安全更新 {label}，配置结构与仓库版本不一致")
    return updated


def update_upstream_text(text: str, origin: str, cdn: str) -> str:
    groups = (("webcoder_origin", origin), ("webcoder_cdn", cdn))
    for group, host in groups:
        pattern = re.compile(
            rf"(upstream\s+{re.escape(group)}\s*\{{.*?\bserver\s+)[^\s:;]+(:443\s*;)",
            re.DOTALL,
        )
        text = replace_exact(text, pattern, rf"\g<1>{host}\g<2>", f"{group} 地址")

    maps = (("overseas_origin_host", origin), ("overseas_cdn_host", cdn))
    for variable, host in maps:
        pattern = re.compile(
            rf"(map\s+\$host\s+\${re.escape(variable)}\s*\{{\s*default\s+)[^;\s]+(\s*;)",
            re.DOTALL,
        )
        text = replace_exact(text, pattern, rf"\g<1>{host}\g<2>", f"{variable} 变量")
    return text


def render_settings(channel: str, account: str, token: str) -> str:
    return (
        "# Managed by the local Overseas Cache administration panel.\n"
        "# This file contains a sensitive token and must remain mode 0600.\n\n"
        f'set $render_channel "{channel}";\n'
        f'set $render_account "{account}";\n'
        f'set $render_token   "{token}";\n'
    )


def atomic_write(path: Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary_name, mode)
        os.chown(temporary_name, 0, 0)
        os.replace(temporary_name, path)
    finally:
        try:
            os.unlink(temporary_name)
        except FileNotFoundError:
            pass


def validate_password(value: object, label: str = "密码") -> str:
    if not isinstance(value, str):
        raise AdminError(f"{label}格式不正确")
    if len(value) < 12 or len(value) > 256:
        raise AdminError(f"{label}长度必须为 12 到 256 个字符")
    return value


def derive_password(password: str, salt: bytes, iterations: int = PASSWORD_ITERATIONS) -> bytes:
    return hashlib.pbkdf2_hmac("sha256", password.encode("utf-8"), salt, iterations)


def render_password_record(username: str, password: str) -> str:
    if not USERNAME_PATTERN.fullmatch(username):
        raise AdminError("管理员用户名格式不正确")
    validate_password(password)
    salt = secrets.token_bytes(32)
    digest = derive_password(password, salt)
    return json.dumps(
        {
            "version": 1,
            "username": username,
            "algorithm": "pbkdf2-sha256",
            "iterations": PASSWORD_ITERATIONS,
            "salt": salt.hex(),
            "digest": digest.hex(),
        },
        separators=(",", ":"),
    ) + "\n"


def read_password_record(path: Path | None = None) -> dict[str, object]:
    selected_path = PASSWORD_CONFIG if path is None else path
    try:
        record = json.loads(read_text(selected_path))
        username = record["username"]
        algorithm = record["algorithm"]
        iterations = record["iterations"]
        salt = bytes.fromhex(record["salt"])
        digest = bytes.fromhex(record["digest"])
    except (AdminError, KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
        raise AdminError("管理员密码文件损坏，请在服务器重新运行安装程序") from exc
    if (
        record.get("version") != 1
        or algorithm != "pbkdf2-sha256"
        or not isinstance(username, str)
        or not USERNAME_PATTERN.fullmatch(username)
        or not isinstance(iterations, int)
        or not 200_000 <= iterations <= 2_000_000
        or len(salt) < 16
        or len(digest) != 32
    ):
        raise AdminError("管理员密码文件格式不受支持")
    return record


def verify_credentials(username: object, password: object, path: Path | None = None) -> bool:
    if not isinstance(username, str) or not isinstance(password, str):
        return False
    try:
        record = read_password_record(path)
        salt = bytes.fromhex(str(record["salt"]))
        expected = bytes.fromhex(str(record["digest"]))
        actual = derive_password(password, salt, int(record["iterations"]))
    except (AdminError, ValueError):
        return False
    username_matches = hmac.compare_digest(username, str(record["username"]))
    password_matches = hmac.compare_digest(actual, expected)
    return username_matches and password_matches


def change_password(current_password: object, new_password: object) -> None:
    with PASSWORD_LOCK:
        record = read_password_record()
        username = str(record["username"])
        if not verify_credentials(username, current_password):
            raise AdminError("当前密码不正确")
        normalized = validate_password(new_password, "新密码")
        if hmac.compare_digest(str(current_password), normalized):
            raise AdminError("新密码不能与当前密码相同")
        atomic_write(PASSWORD_CONFIG, render_password_record(username, normalized), 0o600)


def backup_files() -> Path:
    timestamp = datetime.now(timezone.utc).strftime("admin-%Y%m%dT%H%M%SZ-%f")
    backup_dir = BACKUP_ROOT / timestamp
    backup_dir.mkdir(parents=True, mode=0o700)
    for source in (UPSTREAM_CONFIG, SITE_CONFIG, SETTINGS_CONFIG):
        shutil.copy2(source, backup_dir / source.name)
    return backup_dir


def restore_backup(backup_dir: Path) -> None:
    for destination in (UPSTREAM_CONFIG, SITE_CONFIG, SETTINGS_CONFIG):
        source = backup_dir / destination.name
        shutil.copy2(source, destination)
        os.chown(destination, 0, 0)
    os.chmod(SETTINGS_CONFIG, 0o600)


def apply_config(payload: dict[str, object]) -> tuple[CurrentConfig, Path | None, str]:
    if not isinstance(payload, dict):
        raise AdminError("请求格式不正确")

    LOCK_FILE.parent.mkdir(parents=True, exist_ok=True)
    with LOCK_FILE.open("a+", encoding="utf-8") as lock_handle:
        fcntl.flock(lock_handle.fileno(), fcntl.LOCK_EX)
        current = load_config()
        origin = validate_hostname(payload.get("originHost"), "主站域名")
        cdn = validate_hostname(payload.get("cdnHost"), "CDN 域名")
        channel = validate_id(payload.get("channelId"), "channelId")
        account = validate_id(payload.get("accountId"), "accountId")
        token = validate_token(payload.get("token"), current.token)

        desired = CurrentConfig(origin, cdn, channel, account, token)
        if desired == current:
            return current, None, "配置没有变化"

        backup_dir = backup_files()
        try:
            upstream_text = update_upstream_text(
                read_text(UPSTREAM_CONFIG), desired.origin_host, desired.cdn_host
            )
            atomic_write(UPSTREAM_CONFIG, upstream_text, 0o644)
            atomic_write(
                SETTINGS_CONFIG,
                render_settings(desired.channel_id, desired.account_id, desired.token),
                0o600,
            )
            validation_output = run_checked(["/usr/sbin/nginx", "-t"], timeout=20)
            run_checked(["/usr/bin/systemctl", "reload", "nginx"], timeout=30)
        except Exception as exc:
            try:
                restore_backup(backup_dir)
                run_checked(["/usr/sbin/nginx", "-t"], timeout=20)
                run_checked(["/usr/bin/systemctl", "reload", "nginx"], timeout=30)
            except Exception as rollback_exc:
                raise AdminError(
                    "保存失败，并且自动恢复未能完成。请立即在服务器检查 Nginx。\n"
                    f"保存错误：{exc}\n恢复错误：{rollback_exc}\n备份：{backup_dir}"
                ) from exc
            raise AdminError(f"保存失败，已经恢复旧配置。\n{exc}") from exc

        return desired, backup_dir, validation_output


class NonceStore:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._items: dict[str, float] = {}

    def issue(self) -> str:
        now = time.monotonic()
        token = secrets.token_urlsafe(32)
        with self._lock:
            self._items = {key: expiry for key, expiry in self._items.items() if expiry > now}
            self._items[token] = now + 600
        return token

    def consume(self, token: str) -> bool:
        now = time.monotonic()
        with self._lock:
            expiry = self._items.pop(token, 0)
        return expiry > now


class SessionStore:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._items: dict[str, tuple[str, float]] = {}

    def issue(self, username: str) -> str:
        now = time.monotonic()
        token = secrets.token_urlsafe(48)
        with self._lock:
            self._items = {
                key: value for key, value in self._items.items() if value[1] > now
            }
            self._items[token] = (username, now + SESSION_TTL)
        return token

    def valid(self, token: str) -> bool:
        now = time.monotonic()
        with self._lock:
            item = self._items.get(token)
            if not item or item[1] <= now:
                self._items.pop(token, None)
                return False
        return True

    def revoke(self, token: str) -> None:
        with self._lock:
            self._items.pop(token, None)

    def revoke_all(self) -> None:
        with self._lock:
            self._items.clear()


class LoginLimiter:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._failures: dict[str, tuple[int, float]] = {}

    def allowed(self, address: str) -> tuple[bool, int]:
        now = time.monotonic()
        with self._lock:
            attempts, blocked_until = self._failures.get(address, (0, 0))
            if blocked_until > now:
                return False, max(1, int(blocked_until - now))
            if blocked_until:
                self._failures.pop(address, None)
        return True, 0

    def fail(self, address: str) -> None:
        now = time.monotonic()
        with self._lock:
            attempts, blocked_until = self._failures.get(address, (0, 0))
            if blocked_until > now:
                return
            attempts += 1
            if attempts >= 5:
                self._failures[address] = (0, now + 60)
            else:
                self._failures[address] = (attempts, 0)

    def success(self, address: str) -> None:
        with self._lock:
            self._failures.pop(address, None)


NONCES = NonceStore()
SESSIONS = SessionStore()
LOGIN_LIMITER = LoginLimiter()


class AdminHTTPServer(ThreadingHTTPServer):
    allow_reuse_address = True
    daemon_threads = True
    request_queue_size = 32

    def get_request(self):  # type: ignore[no-untyped-def]
        socket, address = super().get_request()
        socket.settimeout(20)
        return socket, address


class AdminHandler(BaseHTTPRequestHandler):
    server_version = "OverseasCacheAdmin"
    sys_version = ""

    def log_message(self, fmt: str, *args: object) -> None:
        sys.stderr.write(f"[{self.log_date_time_string()}] {self.client_address[0]} {fmt % args}\n")

    def _send_headers(
        self,
        status: int,
        content_type: str,
        length: int,
        extra_headers: tuple[tuple[str, str], ...] = (),
    ) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(length))
        self.send_header("Cache-Control", "no-store")
        self.send_header("X-Content-Type-Options", "nosniff")
        self.send_header("X-Frame-Options", "DENY")
        self.send_header("Referrer-Policy", "no-referrer")
        self.send_header("Cross-Origin-Resource-Policy", "same-origin")
        self.send_header(
            "Content-Security-Policy",
            "default-src 'self'; script-src 'self'; style-src 'self'; "
            "img-src 'self'; connect-src 'self'; base-uri 'none'; "
            "form-action 'self'; frame-ancestors 'none'",
        )
        for name, value in extra_headers:
            self.send_header(name, value)
        self.end_headers()

    def send_bytes(
        self,
        status: int,
        content_type: str,
        body: bytes,
        extra_headers: tuple[tuple[str, str], ...] = (),
    ) -> None:
        self._send_headers(status, content_type, len(body), extra_headers)
        if self.command != "HEAD":
            self.wfile.write(body)

    def send_json(
        self,
        status: int,
        payload: dict[str, object],
        extra_headers: tuple[tuple[str, str], ...] = (),
    ) -> None:
        body = json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
        self.send_bytes(status, "application/json; charset=utf-8", body, extra_headers)

    @property
    def allowed_networks(self) -> tuple[ipaddress.IPv4Network | ipaddress.IPv6Network, ...]:
        return self.server.allowed_networks  # type: ignore[attr-defined]

    def request_host_is_valid(self) -> bool:
        return host_is_allowed(self.headers.get("Host", ""), self.allowed_networks)

    def client_is_valid(self) -> bool:
        # Deliberately use the socket peer address and ignore X-Forwarded-For so
        # a remote client cannot grant itself LAN access with a forged header.
        return address_is_allowed(self.client_address[0], self.allowed_networks)

    def session_token(self) -> str:
        cookie_header = self.headers.get("Cookie", "")
        try:
            cookies = SimpleCookie()
            cookies.load(cookie_header)
            morsel = cookies.get(SESSION_COOKIE)
            return morsel.value if morsel else ""
        except Exception:
            return ""

    def authorized(self) -> bool:
        return SESSIONS.valid(self.session_token())

    def read_json_body(self, maximum: int = 16_384) -> dict[str, object]:
        if self.headers.get_content_type() != "application/json":
            raise AdminError("请求格式必须为 JSON")
        try:
            length = int(self.headers.get("Content-Length", "0"))
        except ValueError:
            length = 0
        if length <= 0 or length > maximum:
            raise AdminError("请求大小不正确")
        try:
            payload = json.loads(self.rfile.read(length))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise AdminError("请求内容不是有效的 JSON") from exc
        if not isinstance(payload, dict):
            raise AdminError("请求格式不正确")
        return payload

    def set_session_cookie(self, token: str, max_age: int = SESSION_TTL) -> tuple[str, str]:
        value = (
            f"{SESSION_COOKIE}={token}; Path=/; Max-Age={max_age}; "
            "HttpOnly; SameSite=Strict"
        )
        return "Set-Cookie", value

    def serve_static(self, path: str) -> bool:
        static_files = {
            "/": ("index.html", "text/html; charset=utf-8"),
            "/index.html": ("index.html", "text/html; charset=utf-8"),
            "/login": ("login.html", "text/html; charset=utf-8"),
            "/login.html": ("login.html", "text/html; charset=utf-8"),
            "/app.css": ("app.css", "text/css; charset=utf-8"),
            "/app.js": ("app.js", "text/javascript; charset=utf-8"),
            "/login.js": ("login.js", "text/javascript; charset=utf-8"),
        }
        selected = static_files.get(path)
        if not selected:
            return False
        filename, content_type = selected
        try:
            body = (STATIC_ROOT / filename).read_bytes()
        except OSError:
            self.send_json(HTTPStatus.INTERNAL_SERVER_ERROR, {"ok": False, "message": "管理页面资源缺失"})
            return True
        self.send_bytes(HTTPStatus.OK, content_type, body)
        return True

    def same_origin_is_valid(self) -> bool:
        return same_origin_is_allowed(
            self.headers.get("Origin", ""),
            self.headers.get("Host", ""),
            self.allowed_networks,
        )

    def do_GET(self) -> None:  # noqa: N802
        if not self.client_is_valid():
            self.send_json(HTTPStatus.FORBIDDEN, {"ok": False, "message": "管理页只允许配置的内网地址访问"})
            return
        if not self.request_host_is_valid():
            self.send_json(HTTPStatus.BAD_REQUEST, {"ok": False, "message": "请使用服务器的内网 IP 打开管理页"})
            return
        path = urlsplit(self.path).path
        if path in {"/login", "/login.html", "/app.css", "/login.js"}:
            self.serve_static(path)
            return
        if path == "/api/session":
            self.send_json(
                HTTPStatus.OK,
                {"ok": True, "authenticated": self.authorized()},
            )
            return
        if not self.authorized():
            if path.startswith("/api/"):
                self.send_json(HTTPStatus.UNAUTHORIZED, {"ok": False, "message": "登录已过期，请重新登录"})
            else:
                self.send_bytes(
                    HTTPStatus.SEE_OTHER,
                    "text/plain; charset=utf-8",
                    b"",
                    (("Location", "/login"),),
                )
            return
        if path == "/api/status":
            self.send_json(
                HTTPStatus.OK,
                {"ok": True, "status": read_fanout_status()},
            )
            return
        if path == "/api/config":
            try:
                config = load_config()
                self.send_json(
                    HTTPStatus.OK,
                    {
                        "ok": True,
                        "csrfToken": NONCES.issue(),
                        "config": {
                            "originHost": config.origin_host,
                            "cdnHost": config.cdn_host,
                            "channelId": config.channel_id,
                            "accountId": config.account_id,
                            "tokenPresent": bool(config.token),
                        },
                    },
                )
            except AdminError as exc:
                self.send_json(
                    HTTPStatus.INTERNAL_SERVER_ERROR,
                    {"ok": False, "message": str(exc), "csrfToken": NONCES.issue()},
                )
            return
        if not self.serve_static(path):
            self.send_json(HTTPStatus.NOT_FOUND, {"ok": False, "message": "页面不存在"})

    def do_POST(self) -> None:  # noqa: N802
        if not self.client_is_valid():
            self.send_json(HTTPStatus.FORBIDDEN, {"ok": False, "message": "管理页只允许配置的内网地址访问"})
            return
        if not self.request_host_is_valid():
            self.send_json(HTTPStatus.BAD_REQUEST, {"ok": False, "message": "请使用服务器的内网 IP 打开管理页"})
            return
        if not self.same_origin_is_valid():
            self.send_json(HTTPStatus.FORBIDDEN, {"ok": False, "message": "来源检查失败，请重新打开管理页"})
            return
        path = urlsplit(self.path).path
        if path == "/api/login":
            allowed, retry_after = LOGIN_LIMITER.allowed(self.client_address[0])
            if not allowed:
                self.send_json(
                    HTTPStatus.TOO_MANY_REQUESTS,
                    {"ok": False, "message": f"登录失败次数过多，请在 {retry_after} 秒后重试"},
                    (("Retry-After", str(retry_after)),),
                )
                return
            try:
                payload = self.read_json_body(4096)
            except AdminError as exc:
                self.send_json(HTTPStatus.BAD_REQUEST, {"ok": False, "message": str(exc)})
                return
            if not verify_credentials(payload.get("username"), payload.get("password")):
                LOGIN_LIMITER.fail(self.client_address[0])
                self.send_json(HTTPStatus.UNAUTHORIZED, {"ok": False, "message": "用户名或密码不正确"})
                return
            LOGIN_LIMITER.success(self.client_address[0])
            token = SESSIONS.issue(str(payload.get("username")))
            self.send_json(
                HTTPStatus.OK,
                {"ok": True, "message": "登录成功"},
                (self.set_session_cookie(token),),
            )
            return
        if not self.authorized():
            self.send_json(HTTPStatus.UNAUTHORIZED, {"ok": False, "message": "登录已过期，请重新登录"})
            return
        if path == "/api/logout":
            SESSIONS.revoke(self.session_token())
            self.send_json(
                HTTPStatus.OK,
                {"ok": True, "message": "已退出登录"},
                (self.set_session_cookie("", 0),),
            )
            return
        if path not in {"/api/config", "/api/password", "/api/discover"}:
            self.send_json(HTTPStatus.NOT_FOUND, {"ok": False, "message": "接口不存在"})
            return
        csrf_token = self.headers.get("X-CSRF-Token", "")
        if not NONCES.consume(csrf_token):
            self.send_json(HTTPStatus.FORBIDDEN, {"ok": False, "message": "页面已过期，请刷新后重试"})
            return
        try:
            payload = self.read_json_body()
            if path == "/api/discover":
                result = discover_site(payload.get("sourceUrl"), payload.get("evidenceUrl", ""))
                self.send_json(
                    HTTPStatus.OK,
                    {
                        "ok": True,
                        "message": "识别完成",
                        "csrfToken": NONCES.issue(),
                        "result": result,
                    },
                )
                return
            if path == "/api/password":
                change_password(payload.get("currentPassword"), payload.get("newPassword"))
                SESSIONS.revoke_all()
                self.send_json(
                    HTTPStatus.OK,
                    {"ok": True, "message": "密码已修改，请使用新密码重新登录"},
                    (self.set_session_cookie("", 0),),
                )
                return
            config, backup_dir, details = apply_config(payload)
            self.send_json(
                HTTPStatus.OK,
                {
                    "ok": True,
                    "message": details if not backup_dir else "配置已保存，Nginx 检查通过并已重新加载",
                    "backup": backup_dir.name if backup_dir else None,
                    "csrfToken": NONCES.issue(),
                    "config": {
                        "originHost": config.origin_host,
                        "cdnHost": config.cdn_host,
                        "channelId": config.channel_id,
                        "accountId": config.account_id,
                        "tokenPresent": bool(config.token),
                    },
                },
            )
        except AdminError as exc:
            self.send_json(HTTPStatus.BAD_REQUEST, {"ok": False, "message": str(exc), "csrfToken": NONCES.issue()})


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", default=23000, type=int)
    parser.add_argument("--init-password-file", type=Path)
    parser.add_argument("--username", default="admin")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if os.geteuid() != 0:
        print("This service must run as root so it can validate and reload Nginx.", file=sys.stderr)
        return 1
    if args.init_password_file:
        password = sys.stdin.read(257)
        if len(password) > 256:
            print("Password is too long.", file=sys.stderr)
            return 1
        try:
            atomic_write(
                args.init_password_file,
                render_password_record(args.username, password),
                0o600,
            )
        except AdminError as exc:
            print(str(exc), file=sys.stderr)
            return 1
        return 0
    try:
        read_password_record()
    except AdminError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    try:
        allowed_networks = parse_allowed_networks(os.environ.get("OVERSEAS_ADMIN_ALLOWED_NETWORKS"))
    except AdminError as exc:
        print(str(exc), file=sys.stderr)
        return 1
    server = AdminHTTPServer((args.host, args.port), AdminHandler)
    server.allowed_networks = allowed_networks  # type: ignore[attr-defined]
    print(f"Overseas Cache Admin listening on http://{args.host}:{args.port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
