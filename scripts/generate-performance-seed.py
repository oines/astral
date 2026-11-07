#!/usr/bin/env python3
"""Create the public, deterministic 227-image seed for Host performance tests.

Only synthetic RGB pixels are generated. The standard-library PNG encoder here
uses fixed filter/compression choices; no user files or model service are read.
"""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import struct
import zlib


def chunk(kind, value):
    return (struct.pack(">I", len(value)) + kind + value
            + struct.pack(">I", zlib.crc32(kind + value) & 0xffffffff))


def image(index):
    width, height = 128, 128
    material = bytearray()
    counter = 0
    while len(material) < width * height * 3:
        material.extend(hashlib.sha256(
            ("astral-public-performance-seed-v1:%03d:%04d" % (index, counter)).encode()).digest())
        counter += 1
    rows = b"".join(b"\x00" + bytes(material[row * width * 3:(row + 1) * width * 3]) for row in range(height))
    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(rows, 6)) + chunk(b"IEND", b""))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    images = [{"type": "image", "media_type": "image/png",
               "data": base64.b64encode(image(index)).decode("ascii")}
              for index in range(227)]
    request = {"messages": [{"role": "user", "content": images}]}
    raw = json.dumps(request, ensure_ascii=False, separators=(",", ":")).encode()
    with args.output.open("xb") as output:
        output.write(raw)
    print(json.dumps({"path": str(args.output.resolve()), "images": len(images),
                      "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest(),
                      "kind": "public synthetic 128x128 RGB PNGs; not the historical private image corpus"}))


if __name__ == "__main__":
    main()
