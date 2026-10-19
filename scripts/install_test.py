"""Offline installer tests: only synthetic artifacts and temporary destinations."""
import copy
import gzip
import hashlib
import io
import json
import os
import pathlib
import stat
import tarfile
import tempfile
import unittest
from unittest import mock
import warnings
import zipfile

import install


def tar_bytes(names=None, kind=None):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.USTAR_FORMAT) as archive:
        for name in names or ["codex-switch"]:
            entry = tarfile.TarInfo(name)
            entry.mode = 0o755
            entry.type = kind or tarfile.REGTYPE
            payload = b"synthetic-new-executable"
            entry.size = len(payload) if entry.type == tarfile.REGTYPE else 0
            if entry.type in (tarfile.SYMTYPE, tarfile.LNKTYPE):
                entry.linkname = "elsewhere"
            archive.addfile(entry, io.BytesIO(payload) if entry.size else None)
    return gzip.compress(buffer.getvalue())


def zip_bytes(names=None, symlink=False):
    buffer = io.BytesIO()
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", UserWarning)
        with zipfile.ZipFile(buffer, "w") as archive:
            for name in names or ["codex-switch.exe"]:
                entry = zipfile.ZipInfo(name)
                entry.external_attr = ((stat.S_IFLNK if symlink else stat.S_IFREG) | 0o755) << 16
                archive.writestr(entry, b"synthetic-new-executable")
    return buffer.getvalue()


class InstallerTests(unittest.TestCase):
    def fixture(self, windows=False):
        tag = "v1.2.3"
        name = "codex-switch_1.2.3_" + ("windows_amd64.zip" if windows else "linux_amd64.tar.gz")
        archive = zip_bytes() if windows else tar_bytes()
        checksum = (hashlib.sha256(archive).hexdigest() + "  " + name + "\n").encode()
        bodies = {install.download_url(tag, name): archive,
                  install.download_url(tag, "checksums.txt"): checksum}
        data = {"tag_name": tag, "html_url": "https://github.com/" + install.REPOSITORY + "/releases/tag/" + tag,
                "draft": False, "prerelease": False, "assets": [
                    {"name": filename, "browser_download_url": address, "size": len(bodies[address])}
                    for filename, address in [(name, install.download_url(tag, name)), ("checksums.txt", install.download_url(tag, "checksums.txt"))]]}
        calls = []

        def fetch(address, limit):
            calls.append(address)
            if address.startswith("https://api.github.com/"):
                return json.dumps(data).encode()
            return bodies[address]
        return data, bodies, calls, fetch

    def test_verified_install_and_explicit_version(self):
        for windows in (False, True):
            for version in ("latest", "v1.2.3", "1.2.3"):
                with self.subTest(windows=windows, version=version), tempfile.TemporaryDirectory() as root:
                    root = str(pathlib.Path(root).resolve())
                    _, _, calls, fetch = self.fixture(windows)
                    target = pathlib.Path(root) / ("codex-switch.exe" if windows else "codex-switch")
                    target.write_bytes(b"synthetic-old-executable")
                    target.chmod(0o755)
                    sentinel = pathlib.Path(root) / "synthetic-account-state"
                    sentinel.write_bytes(b"unchanged")
                    install.perform(version, root, "windows" if windows else "linux", "amd64", fetch)
                    self.assertEqual(target.read_bytes(), b"synthetic-new-executable")
                    self.assertEqual(sentinel.read_bytes(), b"unchanged")
                    self.assertTrue(all(install.REPOSITORY in url for url in calls))
                    if os.name != "nt":
                        self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o755)

    def test_verification_failure_preserves_existing_binary(self):
        for failure in ("wrong-origin", "invalid-version", "missing-asset", "duplicate-archive", "duplicate-checksum", "missing-checksum", "duplicate-line", "bad-checksum", "wrong-size", "oversized", "nested-archive"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as root:
                data, bodies, _, fetch = self.fixture()
                archive, checksum = data["assets"]
                if failure == "wrong-origin": data["html_url"] = data["html_url"].replace(install.REPOSITORY, "SilkageNet/codex-switch")
                if failure == "invalid-version": data["tag_name"] = "v1.2"
                if failure == "missing-asset": data["assets"] = [checksum]
                if failure == "duplicate-archive": data["assets"].append(copy.deepcopy(archive))
                if failure == "duplicate-checksum": data["assets"].append(copy.deepcopy(checksum))
                if failure == "missing-checksum": bodies[checksum["browser_download_url"]] = b"0" * 64 + b"  other.tar.gz\n"
                if failure == "duplicate-line": bodies[checksum["browser_download_url"]] *= 2
                if failure == "bad-checksum": bodies[checksum["browser_download_url"]] = b"0" * 64 + b"  " + archive["name"].encode() + b"\n"
                if failure == "wrong-size": archive["size"] += 1
                if failure == "oversized": archive["size"] = install.ARCHIVE_LIMIT + 1
                if failure == "nested-archive":
                    bodies[archive["browser_download_url"]] = tar_bytes(["nested/codex-switch"])
                    archive["size"] = len(bodies[archive["browser_download_url"]])
                    bodies[checksum["browser_download_url"]] = hashlib.sha256(bodies[archive["browser_download_url"]]).hexdigest().encode() + b"  " + archive["name"].encode() + b"\n"
                checksum["size"] = len(bodies[checksum["browser_download_url"]])
                target = pathlib.Path(root) / "codex-switch"
                target.write_bytes(b"synthetic-old-executable")
                with self.assertRaises(Exception):
                    install.perform("latest", root, "linux", "amd64", fetch)
                self.assertEqual(target.read_bytes(), b"synthetic-old-executable")

    def test_version_origin_and_duplicate_metadata(self):
        for tag in ("", "v1", "v1.2", "v01.2.3", "v1.2.3-rc1", "v1.2.3+build", "../../escape"):
            self.assertFalse(install.valid_tag(tag))
        with self.assertRaises(install.InvalidRelease):
            install.parse_release(b'{"tag_name":"v1.2.3","tag_name":"v9.9.9"}')
        data, _, _, _ = self.fixture()
        with self.assertRaises(install.InvalidRelease):
            install.parse_release(json.dumps(data).encode(), "v9.9.9")
        self.assertFalse(install.trusted_download("http://github.com/" + install.REPOSITORY + "/releases/download/v1.2.3/checksums.txt"))
        self.assertFalse(install.trusted_download("https://github.com/SilkageNet/codex-switch/releases/download/v1.2.3/checksums.txt"))
        self.assertFalse(install.trusted_download("https://github.com:443/" + install.REPOSITORY + "/releases/download/v1.2.3/checksums.txt"))
        self.assertTrue(install.trusted_cdn("https://release-assets.githubusercontent.com/github-production-release-asset/1/2?signature=synthetic"))
        self.assertFalse(install.trusted_cdn("https://evil.example/github-production-release-asset/1/2"))

    def test_archive_paths_duplicates_and_special_entries(self):
        for names in (["nested/codex-switch"], ["../codex-switch"], ["codex-switch", "codex-switch"], ["LICENSE"]):
            with self.assertRaises(install.InvalidRelease): install.extract_binary(tar_bytes(names), False)
        for kind in (tarfile.SYMTYPE, tarfile.LNKTYPE, tarfile.FIFOTYPE):
            with self.assertRaises(install.InvalidRelease): install.extract_binary(tar_bytes(kind=kind), False)
        for names in (["nested/codex-switch.exe"], ["codex-switch.exe", "codex-switch.exe"], ["LICENSE"]):
            with self.assertRaises(install.InvalidRelease): install.extract_binary(zip_bytes(names), True)
        with self.assertRaises(install.InvalidRelease): install.extract_binary(zip_bytes(symlink=True), True)
        with mock.patch.object(install, "BINARY_LIMIT", 1):
            with self.assertRaises(install.InvalidRelease): install.extract_binary(tar_bytes(), False)
        with self.assertRaises(install.InvalidRelease): install.extract_binary(tar_bytes() + tar_bytes(), False)

    def test_failed_atomic_replacement_retains_old_binary(self):
        with tempfile.TemporaryDirectory() as root:
            root = str(pathlib.Path(root).resolve())
            target = pathlib.Path(root) / "codex-switch"
            target.write_bytes(b"synthetic-old-executable")
            with mock.patch.object(install.os, "replace", side_effect=OSError("synthetic failure")):
                with self.assertRaises(OSError): install.install_binary(target, b"synthetic-new-executable")
            self.assertEqual(target.read_bytes(), b"synthetic-old-executable")
            self.assertFalse(list(pathlib.Path(root).glob(".codex-switch-install-*")))

    def test_symlink_target_is_rejected(self):
        with tempfile.TemporaryDirectory() as root:
            root = str(pathlib.Path(root).resolve())
            target = pathlib.Path(root) / "existing"
            target.write_bytes(b"synthetic-old-executable")
            link = pathlib.Path(root) / "codex-switch"
            try:
                link.symlink_to(target)
            except OSError:
                self.skipTest("symlink creation unavailable")
            with self.assertRaises(install.InvalidRelease): install.install_binary(link, b"synthetic-new-executable")
            self.assertEqual(target.read_bytes(), b"synthetic-old-executable")

class FetchPolicyTests(unittest.TestCase):
    def test_redirect_policy_and_bounded_reads(self):
        origin = install.download_url("v1.2.3", "checksums.txt")
        cdn = "https://release-assets.githubusercontent.com/github-production-release-asset/1/2?signature=synthetic"

        class Response(io.BytesIO):
            status = 200

        class Opener:
            def __init__(self, redirect):
                self.redirect = redirect
                self.requests = []

            def open(self, request, timeout):
                self.requests.append(request)
                if len(self.requests) == 1:
                    raise install.urllib.error.HTTPError(request.full_url, 302, "synthetic", {"Location": self.redirect}, None)
                return Response(b"synthetic-data")

        for address in ("https://evil.example/file", "http://release-assets.githubusercontent.com/github-production-release-asset/1/2", "https://github.com/SilkageNet/codex-switch/releases/download/v1.2.3/checksums.txt"):
            opener = Opener(address)
            with mock.patch.object(install.urllib.request, "build_opener", return_value=opener):
                with self.assertRaises(install.InvalidRelease): install.fetch(origin, 1024)
            self.assertEqual(len(opener.requests), 1)
        opener = Opener(cdn)
        with mock.patch.object(install.urllib.request, "build_opener", return_value=opener):
            self.assertEqual(install.fetch(origin, 1024), b"synthetic-data")
        for request in opener.requests:
            self.assertFalse(request.has_header("Authorization"))
            self.assertFalse(request.has_header("Cookie"))
        for address in (install.metadata_url(None), "http://github.com/" + install.REPOSITORY, cdn):
            opener = Opener(cdn)
            with mock.patch.object(install.urllib.request, "build_opener", return_value=opener):
                with self.assertRaises(install.InvalidRelease): install.fetch(address, 1024)
        opener = Opener(cdn)
        with mock.patch.object(install.urllib.request, "build_opener", return_value=opener):
            with self.assertRaises(install.InvalidRelease): install.fetch(origin, 1)

    def test_corrupt_and_trailing_archive_data(self):
        archive = tar_bytes()
        corrupt = bytearray(archive)
        corrupt[-1] ^= 1
        for raw in (bytes(corrupt), archive + archive):
            with self.assertRaises(install.InvalidRelease): install.extract_binary(raw, False)
        expanded = gzip.decompress(archive)
        malicious = gzip.compress(expanded + gzip.decompress(tar_bytes(["codex-switch"])))
        with self.assertRaises(install.InvalidRelease): install.extract_binary(malicious, False)
        with self.assertRaises(install.InvalidRelease):
            install.parse_release(b'{"tag_name":"v1.2.3","TAG_NAME":"v9.9.9"}')


if __name__ == "__main__":
    unittest.main()
