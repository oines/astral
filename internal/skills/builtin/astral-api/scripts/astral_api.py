#!/usr/bin/env python3
"""Call the Astral Computer API from an authorized exec_command."""

import argparse
import http.client
import json
import math
import os
from pathlib import Path
import re
import sys
import tempfile
from contextlib import closing, contextmanager
from urllib.parse import unquote


BODY_LIMIT = 48 * 1024
ERROR_LIMIT = 8 * 1024


class RequestError(Exception):
    pass


def loopback_port(value):
    match = re.fullmatch(r"http://127\.0\.0\.1:([1-9][0-9]{0,4})/?", value)
    if not match or int(match[1]) > 65535:
        raise argparse.ArgumentTypeError("use the current http://127.0.0.1:<port> base URL")
    return int(match[1])


def timeout_seconds(value):
    try:
        seconds = float(value)
    except ValueError:
        seconds = 0
    if not math.isfinite(seconds) or seconds <= 0:
        raise argparse.ArgumentTypeError("timeout must be a positive finite number of seconds")
    return seconds


def api_path(value):
    path = value.partition("?")[0]
    decoded = unquote(path)
    if (
        not path.startswith("/v1/")
        or any(not 33 <= ord(char) <= 126 for char in value)
        or re.search(r"%(?![0-9a-fA-F]{2})", value)
        or "#" in value
        or "\\" in decoded
        or any(ord(char) < 32 or ord(char) == 127 for char in decoded)
        or any(part in (".", "..") for part in decoded.split("/"))
    ):
        raise argparse.ArgumentTypeError("use an origin-relative, URL-encoded /v1/ path and optional query")
    return value


def capability_id(value):
    if not re.fullmatch(r"[a-z][a-z0-9-]*", value):
        raise argparse.ArgumentTypeError("use a capability ID from the Astral capability index")
    return value


def header_value(value):
    if not value.strip() or any(not 32 <= ord(char) <= 126 for char in value):
        raise argparse.ArgumentTypeError("header value must be nonempty printable ASCII")
    return value


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True, type=loopback_port, metavar="URL")
    parser.add_argument("--timeout", type=timeout_seconds, default=60.0, metavar="SECONDS",
                        help="I/O timeout (default: 60 seconds)")
    commands = parser.add_subparsers(dest="command", required=True)
    capabilities = commands.add_parser("capabilities", help="read the capability index or a capability document")
    capabilities.add_argument("id", nargs="?", type=capability_id, metavar="ID")
    request = commands.add_parser("request", help="call a route from the capability document")
    request.add_argument("method", type=str.upper, choices=("GET", "POST", "PATCH", "DELETE"), metavar="METHOD")
    request.add_argument("path", type=api_path, metavar="PATH")
    request.add_argument("--body-file", metavar="FILE|-", help="read a JSON request body from a file or stdin")
    request.add_argument("--idempotency-key", type=header_value, metavar="KEY")
    request.add_argument("--range", type=header_value, metavar="VALUE")
    request.add_argument("--output", type=Path, metavar="FILE", help="save the response to a new file")
    args = parser.parse_args()
    if args.command == "capabilities":
        args.method = "GET"
        args.path = "/v1/capabilities" + ("/" + args.id if args.id else "")
        args.body_file = args.idempotency_key = args.range = args.output = None
    return args


def invalid_json_constant(_value):
    raise ValueError("non-JSON numeric constant")


def read_body(source):
    if source is None:
        return None
    if source == "-":
        body = sys.stdin.buffer.read(BODY_LIMIT + 1)
    else:
        with open(source, "rb") as handle:
            body = handle.read(BODY_LIMIT + 1)
    if len(body) > BODY_LIMIT:
        raise RequestError("JSON request body exceeds the Computer API's 48 KiB limit")
    try:
        json.loads(body.decode("utf-8"), parse_constant=invalid_json_constant)
    except (ValueError, RecursionError) as error:
        raise RequestError("request body must be valid UTF-8 JSON") from error
    return body


@contextmanager
def response_output(target):
    if target is None:
        yield sys.stdout.buffer
        return
    if os.path.lexists(target):
        raise RequestError("output file already exists")
    with tempfile.NamedTemporaryFile(dir=target.parent, prefix=".astral-api-", delete=False) as handle:
        temporary = Path(handle.name)
        try:
            yield handle
            handle.close()
            if os.name == "nt":
                temporary.rename(target)  # On Windows rename fails when target already exists.
            else:
                os.link(temporary, target)  # Publish without replacing a concurrent writer.
        finally:
            handle.close()
            temporary.unlink(missing_ok=True)


def is_text(response):
    kind = (response.getheader("Content-Type") or "").split(";", 1)[0].strip().lower()
    return kind.startswith("text/") or kind in ("application/json", "application/xml") or kind.endswith(("+json", "+xml"))


def http_failure(response):
    message = [f"HTTP {response.status} {response.reason}"]
    for name in ("Retry-After", "Content-Range"):
        if response.getheader(name):
            message.append(f"{name}: {response.getheader(name)}")
    if is_text(response):
        body = response.read(ERROR_LIMIT + 1)
        if body:
            message.append(body[:ERROR_LIMIT].decode("utf-8", errors="replace").strip())
        if len(body) > ERROR_LIMIT:
            message.append("(error body truncated)")
    return RequestError("\n".join(message))


def call(args, token, body, target):
    headers = {"Authorization": "Bearer " + token, "Connection": "close"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    if args.idempotency_key is not None:
        headers["Idempotency-Key"] = args.idempotency_key
    if args.range is not None:
        headers["Range"] = args.range
    # Direct HTTPConnection has no proxy, redirect, cookie or retry machinery.
    with closing(http.client.HTTPConnection("127.0.0.1", args.base_url, timeout=args.timeout)) as connection:
        connection.request(args.method, args.path, body=body, headers=headers)
        with connection.getresponse() as response:
            if not 200 <= response.status < 300:
                raise http_failure(response)
            if args.output is None and response.length != 0 and not is_text(response):
                raise RequestError(f"HTTP {response.status} returned binary content; save it with --output FILE")
            while chunk := response.read1(64 * 1024):
                target.write(chunk)
                if args.output is None:
                    target.flush()
            if response.length not in (None, 0):
                raise RequestError(f"HTTP {response.status} response ended before its declared length")


def main():
    args = arguments()
    token = os.environ.get("ASTRAL_API_TOKEN", "")
    if not re.fullmatch(r"[A-Za-z0-9_-]{43}", token):
        print("Use this script inside an authorized Astral exec_command with ASTRAL_API_TOKEN.", file=sys.stderr)
        return 1
    try:
        body = read_body(args.body_file)
        with response_output(args.output) as target:
            call(args, token, body, target)
    except (RequestError, OSError, OverflowError, http.client.HTTPException) as error:
        message = str(error).replace(token, "[redacted]")
        message = "".join(char if char.isprintable() or char in "\n\t" else "?" for char in message)
        print(message or type(error).__name__, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
