#!/usr/bin/env python3
import json
import os
import socket
import sys


PROJECT_DIR = os.path.dirname(os.path.abspath(__file__))
APP = os.path.join(PROJECT_DIR, "gitskim.py")
RUNTIME_DIR = os.environ.get("XDG_RUNTIME_DIR") or f"/tmp/gitskim-{os.getuid()}"
SOCKET_PATH = os.path.join(RUNTIME_DIR, "gitskim.sock")


def main():
    if len(sys.argv) > 2:
        print(f"usage: {sys.argv[0]} [HEAD|branch|tag|sha]", file=sys.stderr)
        return 2
    request = {"path": os.getcwd(), "revision": sys.argv[1] if len(sys.argv) > 1 else "HEAD"}
    response = ""
    try:
        with socket.socket(socket.AF_UNIX) as client:
            client.settimeout(0.25)
            client.connect(SOCKET_PATH)
            client.sendall(json.dumps(request).encode() + b"\n")
            response = client.recv(16).decode().strip()
    except OSError:
        pass
    mode = "--ephemeral" if response == "BUSY" else "--resident"
    if response != "OK":
        os.execv(sys.executable, [sys.executable, APP, mode, request["path"], request["revision"]])
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
