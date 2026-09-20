import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from update_packages import TARGETS, release_checksums, update_package


class PackageUpdateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.checksums = self.root / "checksums.txt"
        self.hashes = {target: hashlib.sha256(target.encode()).hexdigest() for target in TARGETS}
        self.write_checksums("0.6.0")
        self.formula = self.root / "chill.rb"
        self.manifest = self.root / "chill.json"
        # Include the hand-maintained fields the updater must preserve.
        formula = 'class Chill < Formula\n  version "0.5.0"\n  depends_on "mpv"\n'
        for target, extension in TARGETS.items():
            if not target.startswith("windows_"):
                formula += (f'  url "https://github.com/willibrandon/chill/releases/download/'
                            f'v#{{version}}/chill_#{{version}}_{target}.{extension}"\n'
                            f'  sha256 "{"0" * 64}"\n')
        self.formula.write_text(formula + "end\n", encoding="utf-8")
        self.manifest.write_text(json.dumps({
            "version": "0.5.0",
            "depends": ["extras/mpv", "main/yt-dlp", "main/deno"],
            "architecture": {"64bit": {}, "arm64": {}},
            "bin": "chill.exe",
            "autoupdate": {"hash": {"url": "$baseurl/checksums.txt"}},
        }, indent=4) + "\n", encoding="utf-8")

    def write_checksums(self, version):
        self.checksums.write_text("".join(
            f"{self.hashes[target]}  chill_{version}_{target}.{extension}\n"
            for target, extension in TARGETS.items()
        ), encoding="utf-8")

    def test_updates_every_platform_and_preserves_manual_fields(self):
        update_package("v0.6.0", self.checksums, self.formula, "homebrew")
        formula = self.formula.read_text(encoding="utf-8")
        self.assertIn('version "0.6.0"', formula)
        self.assertIn('depends_on "mpv"', formula)
        self.assertIn('depends_on "ffmpeg"', formula)
        for target in TARGETS:
            if not target.startswith("windows_"):
                self.assertIn(f'{target}.tar.gz"\n  sha256 "{self.hashes[target]}"', formula)

        update_package("v0.6.0", self.checksums, self.manifest, "scoop")
        manifest = json.loads(self.manifest.read_text(encoding="utf-8"))
        self.assertEqual(manifest["version"], "0.6.0")
        self.assertEqual(manifest["depends"], ["extras/mpv", "main/yt-dlp", "main/deno", "main/ffmpeg"])
        self.assertEqual(manifest["bin"], "chill.exe")
        self.assertEqual(manifest["autoupdate"], {"hash": {"url": "$baseurl/checksums.txt"}})
        for arch, target in {"64bit": "windows_amd64", "arm64": "windows_arm64"}.items():
            entry = manifest["architecture"][arch]
            self.assertEqual(entry["hash"], self.hashes[target])
            self.assertEqual(entry["extract_dir"], f"chill_0.6.0_{target}")
            self.assertEqual(entry["url"], "https://github.com/willibrandon/chill/releases/"
                             f"download/v0.6.0/chill_0.6.0_{target}.zip")

    def test_reruns_are_idempotent_and_older_releases_do_not_downgrade(self):
        self.write_checksums("0.10.0")
        for path, kind in [(self.formula, "homebrew"), (self.manifest, "scoop")]:
            with self.subTest(kind=kind):
                update_package("v0.10.0", self.checksums, path, kind)
                expected = path.read_bytes()
                update_package("v0.10.0", self.checksums, path, kind)
                self.assertEqual(path.read_bytes(), expected)
                self.write_checksums("0.9.0")
                update_package("v0.9.0", self.checksums, path, kind)
                self.assertEqual(path.read_bytes(), expected)
                self.write_checksums("0.10.0")

    def test_rebuilt_release_updates_hashes_at_the_same_version(self):
        for path, kind in [(self.formula, "homebrew"), (self.manifest, "scoop")]:
            update_package("v0.6.0", self.checksums, path, kind)
        self.hashes = {target: "a" * 64 for target in TARGETS}
        self.write_checksums("0.6.0")
        for path, kind in [(self.formula, "homebrew"), (self.manifest, "scoop")]:
            update_package("v0.6.0", self.checksums, path, kind)
            self.assertIn("a" * 64, path.read_text(encoding="utf-8"))

    def test_rejects_invalid_tags_without_changing_files(self):
        for tag in ["v0.6.0-rc.1", "0.6.0", "v0.6", "v01.2.3", "v0.6.0\n"]:
            for path, kind in [(self.formula, "homebrew"), (self.manifest, "scoop")]:
                with self.subTest(tag=tag, kind=kind):
                    original = path.read_bytes()
                    with self.assertRaises(ValueError):
                        update_package(tag, self.checksums, path, kind)
                    self.assertEqual(path.read_bytes(), original)

    def test_rejects_incomplete_or_invalid_checksums_without_changes(self):
        original_checksums = self.checksums.read_text(encoding="utf-8")
        for contents in ["", original_checksums.splitlines()[0], original_checksums * 2,
                         original_checksums.replace(self.hashes["linux_arm64"], "invalid")]:
            self.checksums.write_text(contents, encoding="utf-8")
            for path, kind in [(self.formula, "homebrew"), (self.manifest, "scoop")]:
                original = path.read_bytes()
                with self.assertRaises(ValueError):
                    update_package("v0.6.0", self.checksums, path, kind)
                self.assertEqual(path.read_bytes(), original)

    def test_formula_layout_changes_fail_without_partial_updates(self):
        original = self.formula.read_text(encoding="utf-8").replace("linux_arm64", "linux_unknown")
        self.formula.write_text(original, encoding="utf-8")
        with self.assertRaises(ValueError):
            update_package("v0.6.0", self.checksums, self.formula, "homebrew")
        self.assertEqual(self.formula.read_text(encoding="utf-8"), original)

    def test_accepts_binary_checksum_format(self):
        text = self.checksums.read_text(encoding="utf-8").replace("  chill", " *chill")
        self.assertEqual(release_checksums(text, "0.6.0"), self.hashes)


if __name__ == "__main__":
    unittest.main()
