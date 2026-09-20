"""Update a chill package definition from a published release's checksums."""

import argparse
import json
from pathlib import Path
import re


TARGETS = {
    "darwin_arm64": "tar.gz",
    "darwin_amd64": "tar.gz",
    "linux_arm64": "tar.gz",
    "linux_amd64": "tar.gz",
    "windows_amd64": "zip",
    "windows_arm64": "zip",
}


def version_tuple(version):
    if not re.fullmatch(r"(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)", version):
        raise ValueError(f"expected a stable version, got {version!r}")
    return tuple(map(int, version.split(".")))


def release_checksums(text, version):
    checksums = {}
    for line in text.splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r"([0-9a-fA-F]{64})\s+\*?(\S+)", line)
        if not match:
            raise ValueError(f"invalid checksum line: {line!r}")
        digest, name = match.groups()
        if name in checksums:
            raise ValueError(f"duplicate checksum for {name}")
        checksums[name] = digest.lower()
    result = {}
    for target, extension in TARGETS.items():
        name = f"chill_{version}_{target}.{extension}"
        if name not in checksums:
            raise ValueError(f"missing checksum for {name}")
        result[target] = checksums[name]
    return result


def replace_once(pattern, replacement, text):
    updated, count = re.subn(pattern, replacement, text, flags=re.MULTILINE)
    if count != 1:
        raise ValueError(f"expected one match for {pattern!r}, found {count}")
    return updated


def update_homebrew(text, version, checksums):
    versions = re.findall(r'^  version "([^"]+)"$', text, re.MULTILINE)
    if len(versions) != 1:
        raise ValueError("expected exactly one Homebrew version")
    if version_tuple(versions[0]) > version_tuple(version):
        return text
    updated = replace_once(r'^  version "[^"]+"$', f'  version "{version}"', text)
    if not re.search(r'^  depends_on "ffmpeg"$', updated, re.MULTILINE):
        updated = replace_once(r'^  depends_on "mpv"$',
                               '  depends_on "mpv"\n  depends_on "ffmpeg"', updated)
    for target, extension in TARGETS.items():
        if target.startswith("windows_"):
            continue
        url = ("https://github.com/willibrandon/chill/releases/download/"
               f"v#{{version}}/chill_#{{version}}_{target}.{extension}")
        pattern = rf'(url "{re.escape(url)}"\s+sha256 ")[0-9a-fA-F]{{64}}(")'
        updated = replace_once(pattern, rf"\g<1>{checksums[target]}\g<2>", updated)
    return updated


def update_scoop(text, version, checksums):
    manifest = json.loads(text)
    if version_tuple(manifest["version"]) > version_tuple(version):
        return text
    architectures = {"64bit": "windows_amd64", "arm64": "windows_arm64"}
    if set(manifest["architecture"]) != set(architectures):
        raise ValueError("expected Scoop x64 and ARM64 architectures")
    manifest["version"] = version
    dependencies = manifest.setdefault("depends", [])
    if "main/ffmpeg" not in dependencies:
        dependencies.append("main/ffmpeg")
    for arch, target in architectures.items():
        base = f"chill_{version}_{target}"
        manifest["architecture"][arch].update({
            "url": f"https://github.com/willibrandon/chill/releases/download/v{version}/{base}.zip",
            "hash": checksums[target],
            "extract_dir": base,
        })
    return json.dumps(manifest, indent=4) + "\n"


def update_package(tag, checksum_path, path, kind):
    if not tag.startswith("v"):
        raise ValueError("release tag must start with v")
    version = tag[1:]
    version_tuple(version)
    checksums = release_checksums(checksum_path.read_text(encoding="utf-8"), version)
    original = path.read_text(encoding="utf-8")
    update = update_homebrew if kind == "homebrew" else update_scoop
    updated = update(original, version, checksums)
    if updated == original:
        print(f"{path} is already current or newer than {tag}")
        return
    path.write_text(updated, encoding="utf-8")
    print(f"Updated {path} to {tag}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--checksums", required=True, type=Path)
    package = parser.add_mutually_exclusive_group(required=True)
    package.add_argument("--homebrew", type=Path)
    package.add_argument("--scoop", type=Path)
    args = parser.parse_args()
    try:
        update_package(args.tag, args.checksums, args.homebrew or args.scoop,
                       "homebrew" if args.homebrew else "scoop")
    except (ValueError, KeyError, OSError) as error:
        parser.exit(1, f"Package update failed: {error}\n")


if __name__ == "__main__":
    main()
