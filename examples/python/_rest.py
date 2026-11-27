"""Small REST helper for examples that also run with the mock backend."""

import json
import os
from contextlib import contextmanager
from urllib.parse import quote, urlencode, urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Do not forward the gateway API key to a redirected destination.
        return None


def request(method, path, status, body=None):
    endpoint = os.environ.get("E2B_API_URL") or os.environ.get("E2B_DOMAIN") or "localhost:8080"
    if "://" not in endpoint:
        host = urlsplit("//" + endpoint).hostname
        scheme = "http" if host in ("localhost", "127.0.0.1", "::1") else "https"
        endpoint = scheme + "://" + endpoint
    url = urlsplit(endpoint)
    if url.scheme not in ("http", "https") or not url.hostname or url.username is not None or url.query or url.fragment:
        raise ValueError("set E2B_API_URL or E2B_DOMAIN to a gateway HTTP(S) URL")
    headers = {"X-API-Key": os.environ.get("E2B_API_KEY", "test-key")}
    data = None
    if body is not None:
        headers["Content-Type"] = "application/json"
        data = json.dumps(body).encode("utf-8")
    req = Request(endpoint.rstrip("/") + path, data, headers, method=method)
    with build_opener(NoRedirects()).open(req, timeout=15) as response:
        if response.status != status:
            raise RuntimeError(f"{method} {path}: expected HTTP {status}, got {response.status}")
        payload = response.read()
        return json.loads(payload) if payload else None


def segment(value):
    return quote(value, safe="")


@contextmanager
def sandbox():
    result = request("POST", "/sandboxes", 201, {"templateID": "base", "timeout": 120})
    path = "/sandboxes/" + segment(result["sandboxID"])
    try:
        yield path
    finally:
        request("DELETE", path, 204)
        print("Sandbox killed.")


def hello_world():
    with sandbox() as path:
        marker = "Hello from E2BGateway!"
        result = request("POST", path + "/commands", 200, {"command": f"echo '{marker}'"})
        if result["exitCode"] != 0 or marker not in result["stdout"]:
            raise RuntimeError(f"unexpected command result: {result}")
        print(result["stdout"].strip())


def code_execution():
    with sandbox() as path:
        marker = "Hello from code execution!"
        result = request("POST", path + "/code", 200, {"code": f"print('{marker}')", "language": "python"})
        if result["exitCode"] != 0 or result.get("error") or marker not in result["stdout"]:
            raise RuntimeError(f"unexpected code result: {result}")
        print(result["stdout"].strip())


def filesystem():
    with sandbox() as path:
        filename = "/tmp/gateway example & test.txt"
        text = "Hello from E2BGateway!"
        request("POST", path + "/files", 204, {"path": filename, "content": text})
        result = request("GET", path + "/files?" + urlencode({"path": filename}), 200)
        if result != text:
            raise RuntimeError(f"file contents mismatch: {result!r}")
        print(f"Read back: {result}")
