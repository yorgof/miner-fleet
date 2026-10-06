#!/usr/bin/env python3
"""Package only explicit release binaries and their documentation."""

import argparse
import hashlib
from pathlib import Path
import re
import tarfile
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binaries", type=Path)
    parser.add_argument("output", type=Path)
    parser.add_argument("version")
    args = parser.parse_args()
    if not re.fullmatch(r"dev|[0-9]+\.[0-9]+\.[0-9]+", args.version):
        parser.error("version must be dev or MAJOR.MINOR.PATCH")

    root = Path(__file__).resolve().parent.parent
    documents = [root / name for name in ("LICENSE", "THIRD_PARTY_NOTICES.md", "README.md")]
    targets = [(os_name, arch) for os_name in ("linux", "darwin", "windows")
               for arch in ("amd64", "arm64")]
    # Validate all inputs before creating any archives; never glob a data folder.
    inputs = []
    for os_name, arch in targets:
        suffix = ".exe" if os_name == "windows" else ""
        binary = args.binaries / f"miner-fleet-{os_name}-{arch}{suffix}"
        for source in [binary, *documents]:
            if not source.is_file() or source.is_symlink():
                parser.error(f"missing or unsafe release input: {source}")
        inputs.append((os_name, arch, binary, f"miner-fleet{suffix}"))

    args.output.mkdir(parents=True, exist_ok=True)
    checksums = []
    for os_name, arch, binary, binary_name in inputs:
        folder = f"miner-fleet-{args.version}-{os_name}-{arch}"
        members = [(binary, binary_name), *((path, path.name) for path in documents)]
        if os_name == "windows":
            archive = args.output / f"{folder}.zip"
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as package:
                for source, name in members:
                    package.write(source, f"{folder}/{name}")
        else:
            archive = args.output / f"{folder}.tar.gz"
            with tarfile.open(archive, "w:gz") as package:
                for source, name in members:
                    info = package.gettarinfo(str(source), arcname=f"{folder}/{name}")
                    info.uid = info.gid = 0
                    info.uname = info.gname = ""
                    info.mode = 0o755 if source == binary else 0o644
                    with source.open("rb") as content:
                        package.addfile(info, content)
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        checksums.append(f"{digest}  {archive.name}\n")
    (args.output / "SHA256SUMS").write_text("".join(sorted(checksums)), encoding="utf-8")
    print(f"Packaged {len(inputs)} targets with MIT license, notices and SHA256SUMS.")


if __name__ == "__main__":
    main()
