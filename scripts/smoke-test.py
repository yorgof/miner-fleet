#!/usr/bin/env python3
"""Launch a native release binary with isolated, empty state and check its UI."""

import argparse
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", type=Path)
    parser.add_argument("--version", default="dev")
    args = parser.parse_args()
    binary = args.binary.resolve()
    # Never inherit device/dashboard credentials or notification configuration.
    env = {key: value for key, value in os.environ.items()
           if not key.startswith("MINER_FLEET_")}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    with tempfile.TemporaryDirectory(prefix="miner-fleet-smoke-") as work:
        database = Path(work) / "miner-fleet.db"
        output = subprocess.check_output(
            [str(binary), "-version", f"-db={database}"], env=env, text=True, timeout=10)
        if output.strip() != f"Miner Fleet {args.version}" or database.exists():
            raise RuntimeError("Version flag failed or created persistent state")
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        address = f"127.0.0.1:{port}"
        with (Path(work) / "startup.log").open("w+") as log:
            process = subprocess.Popen(
                [str(binary), f"-addr={address}", f"-db={database}"],
                env=env, stdout=log, stderr=log)
            try:
                deadline = time.monotonic() + 30
                while True:
                    if process.poll() is not None:
                        log.seek(0)
                        raise RuntimeError(f"Binary exited before startup: {log.read()}")
                    try:
                        with opener.open(f"http://{address}/healthz", timeout=1) as reply:
                            if reply.status != 200:
                                raise RuntimeError("Healthcheck failed")
                        break
                    except (urllib.error.URLError, TimeoutError):
                        if time.monotonic() >= deadline:
                            raise RuntimeError("Startup timed out")
                        time.sleep(0.1)
                with opener.open(f"http://{address}/", timeout=5) as reply:
                    if reply.status != 200 or "Miner Fleet" not in reply.read().decode():
                        raise RuntimeError("Dashboard did not render")
                subprocess.run([str(binary), f"-addr={address}", "-healthcheck"],
                               env=env, check=True, timeout=10)
                if not database.is_file() or not Path(str(database) + ".key").is_file():
                    raise RuntimeError("Database or encryption key missing")
                if os.name != "nt":
                    for path in (database, Path(str(database) + ".key")):
                        if path.stat().st_mode & 0o777 != 0o600:
                            raise RuntimeError("Persistent state permissions are not 0600")
            finally:
                if process.poll() is None:
                    process.terminate()
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
    print(f"Native startup, version, healthcheck, dashboard and fresh state OK ({args.version}).")


if __name__ == "__main__":
    main()
