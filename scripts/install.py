#!/usr/bin/env python3
"""Verify fork release artifacts before touching an installation (Python 3.9+)."""
import contextlib
import hashlib
import io
import json
import os
import pathlib
import platform
import re
import stat
import sys
import tarfile
import tempfile
import urllib.error
import urllib.parse
import urllib.request
import zipfile
import zlib

REPOSITORY = "hudrucan/codex-switch"
METADATA_LIMIT = 2 << 20
CHECKSUM_LIMIT = 1 << 20
ARCHIVE_LIMIT = 128 << 20
BINARY_LIMIT = 64 << 20
VERSION = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
NAME = re.compile(r"[A-Za-z0-9._-]{1,200}\Z")


class InvalidRelease(Exception):
    pass


def require(condition, message):
    if not condition:
        raise InvalidRelease(message)


def valid_tag(tag):
    return isinstance(tag, str) and len(tag) <= 64 and VERSION.fullmatch(tag)


def asset_name(name):
    return isinstance(name, str) and NAME.fullmatch(name) and ".." not in name


def metadata_url(tag):
    suffix = "latest" if tag is None else "tags/" + tag
    return "https://api.github.com/repos/" + REPOSITORY + "/releases/" + suffix


def download_url(tag, name):
    return "https://github.com/" + REPOSITORY + "/releases/download/" + tag + "/" + name


def trusted_download(address):
    value = urllib.parse.urlsplit(address)
    parts = value.path.split("/")
    return (value.scheme == "https" and value.netloc == "github.com" and not value.query
            and not value.fragment and len(parts) == 7 and "/".join(parts[1:5]) == REPOSITORY + "/releases/download"
            and valid_tag(parts[5]) and asset_name(parts[6]))


def trusted_cdn(address):
    value = urllib.parse.urlsplit(address)
    return (value.scheme == "https" and value.netloc == "release-assets.githubusercontent.com"
            and not value.fragment and value.path.startswith("/github-production-release-asset/")
            and ".." not in value.path and "%" not in value.path)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, stream, code, message, headers, address):
        return None


def fetch(address, limit):
    allowed_metadata = address == metadata_url(None)
    prefix = metadata_url(None).removesuffix("latest") + "tags/"
    if address.startswith(prefix):
        allowed_metadata = bool(valid_tag(address[len(prefix):]))
    require(allowed_metadata or trusted_download(address), "Untrusted release URL")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    for redirect in range(2):
        request = urllib.request.Request(address, headers={"User-Agent": "codex-switch-installer"})
        try:
            response = opener.open(request, timeout=45)
        except urllib.error.HTTPError as error:
            if error.code in (301, 302, 303, 307, 308):
                location = urllib.parse.urljoin(address, error.headers.get("Location", ""))
                error.close()
                require(redirect == 0 and trusted_download(address) and trusted_cdn(location), "Untrusted release redirect")
                address = location
                continue
            error.close()
            raise InvalidRelease("Release request failed") from None
        except (OSError, ValueError):
            raise InvalidRelease("Release request failed") from None
        with response:
            require(response.status == 200, "Release request failed")
            data = response.read(limit + 1)
        require(0 < len(data) <= limit, "Release data exceeds its size limit or is empty")
        return data
    raise InvalidRelease("Too many release redirects")


def unique_object(pairs):
    result = {}
    seen = set()
    for key, value in pairs:
        require(key.lower() not in seen, "Ambiguous release metadata")
        seen.add(key.lower())
        result[key] = value
    return result


def parse_release(raw, requested=None):
    require(len(raw) <= METADATA_LIMIT, "Release metadata exceeds its size limit")
    data = json.loads(raw, object_pairs_hook=unique_object)
    tag = data.get("tag_name")
    require(valid_tag(tag) and (requested is None or tag == requested), "Invalid stable release version")
    require(data.get("html_url") == "https://github.com/" + REPOSITORY + "/releases/tag/" + tag
            and data.get("draft", False) is False and data.get("prerelease", False) is False, "Untrusted release origin")
    assets = data.get("assets")
    require(isinstance(assets, list) and 0 < len(assets) <= 256, "Invalid release assets")
    selected = {}
    for entry in assets:
        name, size = entry.get("name"), entry.get("size")
        require(asset_name(name) and name not in selected and type(size) is int and 0 < size <= ARCHIVE_LIMIT
                and entry.get("browser_download_url") == download_url(tag, name), "Invalid or duplicate release asset")
        selected[name] = entry
    return tag, selected


def checksum_for(raw, name):
    require(len(raw) <= CHECKSUM_LIMIT, "Checksum manifest exceeds its size limit")
    records = {}
    for line in raw.decode("ascii").splitlines():
        if not line.strip():
            continue
        match = re.fullmatch(r"([0-9a-fA-F]{64})[ \t]+\*?([A-Za-z0-9._-]+)", line.strip())
        require(match is not None, "Malformed checksum manifest")
        digest, filename = match.groups()
        require(asset_name(filename) and filename not in records, "Ambiguous checksum manifest")
        records[filename] = bytes.fromhex(digest)
    require(name in records, "Archive checksum missing")
    return records[name]


def extract_binary(raw, windows):
    require(0 < len(raw) <= ARCHIVE_LIMIT, "Invalid archive size")
    expected = "codex-switch.exe" if windows else "codex-switch"
    seen, total, binary = set(), 0, None

    def check(name, directory, size):
        nonlocal total
        clean = name[:-1] if name.endswith("/") else name
        parts = clean.split("/")
        require(clean and all(asset_name(part) for part in parts) and ":" not in clean
                and clean.lower() not in seen and len(seen) < 256, "Unsafe or duplicate archive path")
        seen.add(clean.lower())
        if directory:
            require(clean == "LICENSES" and size == 0, "Unexpected archive directory")
            return
        require(name == clean and (clean in (expected, "LICENSE", "README.md", "THIRD_PARTY_NOTICES.md")
                or (clean.startswith("LICENSES/") and parts[-1] not in ("codex-switch", "codex-switch.exe"))), "Unexpected archive file")
        require(0 < size <= BINARY_LIMIT and total + size <= ARCHIVE_LIMIT, "Archive entry exceeds its size limit")
        total += size

    if windows:
        with zipfile.ZipFile(io.BytesIO(raw)) as archive:
            for entry in archive.infolist():
                kind = stat.S_IFMT(entry.external_attr >> 16)
                require(kind in (0, stat.S_IFREG, stat.S_IFDIR), "Special archive entry rejected")
                check(entry.filename, entry.is_dir(), entry.file_size)
                if not entry.is_dir():
                    with archive.open(entry) as stream:
                        contents = stream.read(BINARY_LIMIT + 1)
                    require(len(contents) == entry.file_size, "Invalid archive entry")
                    if entry.filename == expected:
                        binary = contents
    else:
        decoder = zlib.decompressobj(31)
        try:
            expanded = decoder.decompress(raw, ARCHIVE_LIMIT + (1 << 20) + 1)
        except zlib.error:
            raise InvalidRelease("Invalid gzip data") from None
        require(decoder.eof and not decoder.unused_data and len(expanded) <= ARCHIVE_LIMIT + (1 << 20), "Invalid or oversized gzip data")
        with tarfile.open(fileobj=io.BytesIO(expanded), mode="r:") as archive:
            for entry in archive:
                raw_header = expanded[entry.offset:entry.offset + 512]
                require(len(raw_header) == 512 and raw_header[257:263] == b"ustar\x00"
                        and raw_header[156:157] in (b"0", b"\x00", b"5"), "Extended tar headers rejected")
                require((entry.type == tarfile.REGTYPE or entry.isdir()) and not entry.pax_headers, "Special archive entry rejected")
                check(entry.name, entry.isdir(), entry.size)
                if not entry.isdir():
                    with archive.extractfile(entry) as stream:
                        contents = stream.read(BINARY_LIMIT + 1)
                    require(len(contents) == entry.size, "Invalid archive entry")
                    if entry.name == expected:
                        binary = contents
            require(not expanded[archive.offset:].strip(b"\x00"), "Unexpected data after tar terminator")
    require(binary is not None, "Expected executable missing from archive")
    return binary


@contextlib.contextmanager
def target_lock(target):
    lock = target.with_name(target.name + ".update.lock")
    require(not lock.is_symlink() and (not lock.exists() or lock.is_file()), "Unsafe installation lock")
    descriptor = os.open(lock, os.O_CREAT | os.O_RDWR | getattr(os, "O_NOFOLLOW", 0), 0o600)
    try:
        if os.name == "nt":
            import msvcrt
            if os.fstat(descriptor).st_size == 0:
                os.write(descriptor, b"0")
            os.lseek(descriptor, 0, os.SEEK_SET)
            msvcrt.locking(descriptor, msvcrt.LK_NBLCK, 1)
        else:
            import fcntl
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield
    finally:
        os.close(descriptor)


def install_binary(target, binary):
    require(0 < len(binary) <= BINARY_LIMIT, "Invalid executable size")
    target = pathlib.Path(os.path.abspath(target))
    require(target.resolve() == target, "Unsafe installation path")
    target.parent.mkdir(parents=True, exist_ok=True)
    with target_lock(target):
        original = target.lstat() if target.exists() or target.is_symlink() else None
        require(original is None or (stat.S_ISREG(original.st_mode) and not original.st_mode & (stat.S_ISUID | stat.S_ISGID)), "Unsafe installation target")
        def read_existing():
            with target.open("rb") as stream:
                data = stream.read(BINARY_LIMIT + 1)
            require(0 < len(data) <= BINARY_LIMIT, "Existing executable exceeds its size limit")
            return data
        old = read_existing() if original else None
        descriptor, temporary = tempfile.mkstemp(prefix=".codex-switch-install-", dir=target.parent)
        try:
            with os.fdopen(descriptor, "wb") as stream:
                if os.name != "nt":
                    os.fchmod(stream.fileno(), stat.S_IMODE(original.st_mode) if original else 0o755)
                stream.write(binary)
                stream.flush()
                os.fsync(stream.fileno())
            require((read_existing() if target.exists() else None) == old, "Installation target changed")
            if original:
                current = target.lstat()
                require((original.st_dev, original.st_ino, original.st_mode) == (current.st_dev, current.st_ino, current.st_mode), "Installation target changed")
            else:
                require(not target.is_symlink(), "Installation target changed")
            os.replace(temporary, target)
            if os.name != "nt":
                directory = os.open(target.parent, os.O_RDONLY)
                try:
                    os.fsync(directory)
                finally:
                    os.close(directory)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)


def perform(version, destination, goos, arch, fetcher=fetch):
    require(goos in ("darwin", "linux", "windows") and arch in ("amd64", "arm64"), "Unsupported platform")
    requested = None if version == "latest" else (version if version.startswith("v") else "v" + version)
    require(requested is None or valid_tag(requested), "Invalid release version")
    tag, assets = parse_release(fetcher(metadata_url(requested), METADATA_LIMIT), requested)
    name = "codex-switch_" + tag[1:] + "_" + goos + "_" + arch + (".zip" if goos == "windows" else ".tar.gz")
    require(name in assets and "checksums.txt" in assets, "Required release assets missing")
    def download(entry, limit):
        require(entry["size"] <= limit, "Release asset exceeds its size limit")
        data = fetcher(entry["browser_download_url"], limit)
        require(len(data) == entry["size"] and len(data) <= limit, "Release asset size mismatch")
        return data
    expected = checksum_for(download(assets["checksums.txt"], CHECKSUM_LIMIT), name)
    archive = download(assets[name], ARCHIVE_LIMIT)
    require(hashlib.sha256(archive).digest() == expected, "Archive checksum mismatch")
    binary = extract_binary(archive, goos == "windows")
    install_binary(pathlib.Path(destination) / ("codex-switch.exe" if goos == "windows" else "codex-switch"), binary)


def main():
    if sys.version_info < (3, 9):
        print("Python 3.9+ is required for verified installation", file=sys.stderr)
        return 1
    try:
        system = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}.get(platform.system(), "unsupported")
        arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64", "ARM64": "arm64"}.get(platform.machine(), "unsupported")
        default = pathlib.Path(os.environ.get("LOCALAPPDATA", "")) / "Programs/codex-switch" if system == "windows" else pathlib.Path.home() / ".local/bin"
        perform(os.environ.get("CODEX_SWITCH_VERSION", "latest"), os.environ.get("CODEX_SWITCH_INSTALL_DIR", str(default)), system, arch)
        print("Installed verified fork release from " + REPOSITORY)
    except Exception:
        print("Installation failed; release verification or atomic installation could not be confirmed. Inspect the installed version before retrying.", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
