"""Release policy tests using isolated local Git repositories, without publishing."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
VERIFY = ROOT / "scripts" / "verify-release-source.sh"


class ReleaseSourceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.env = os.environ.copy()
        for name in list(self.env):
            if name.startswith("GIT_"):
                del self.env[name]
        self.env.update({
            "HOME": str(self.root),
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_TERMINAL_PROMPT": "0",
            "GIT_ALLOW_PROTOCOL": "file",
            "GIT_AUTHOR_NAME": "Synthetic Release Test",
            "GIT_AUTHOR_EMAIL": "release@example.invalid",
            "GIT_COMMITTER_NAME": "Synthetic Release Test",
            "GIT_COMMITTER_EMAIL": "release@example.invalid",
        })
        self.seed = self.root / "seed"
        self.origin = self.root / "origin.git"
        self.work = self.root / "work"
        self.git(self.root, "init", "--initial-branch=main", str(self.seed))
        self.git(self.seed, "commit", "--allow-empty", "-m", "Synthetic base")
        self.base = self.git(self.seed, "rev-parse", "HEAD").stdout.strip()
        self.git(self.seed, "tag", "v1.0.0")
        self.git(self.seed, "commit", "--allow-empty", "-m", "Synthetic main")
        self.main = self.git(self.seed, "rev-parse", "HEAD").stdout.strip()
        self.git(self.seed, "checkout", "-b", "outside", self.base)
        self.git(self.seed, "commit", "--allow-empty", "-m", "Synthetic outside main")
        self.outside = self.git(self.seed, "rev-parse", "HEAD").stdout.strip()
        self.git(self.seed, "tag", "v2.0.0")
        self.git(self.seed, "checkout", "main")
        self.git(self.root, "clone", "--bare", str(self.seed), str(self.origin))
        self.git(self.root, "clone", "--no-local", str(self.origin), str(self.work))

    def git(self, cwd, *args):
        return subprocess.run(["git", *args], cwd=cwd, env=self.env,
                              capture_output=True, text=True, check=True)

    def verify(self, tag):
        return subprocess.run(["bash", str(VERIFY), tag], cwd=self.work,
                              env=self.env, capture_output=True, text=True)

    def test_tag_in_main_history_is_accepted(self):
        self.git(self.work, "checkout", "--detach", "v1.0.0")
        self.assertEqual(self.verify("v1.0.0").returncode, 0)

    def test_tag_outside_main_is_rejected_even_with_stale_local_ref(self):
        self.git(self.work, "checkout", "--detach", "v2.0.0")
        self.git(self.work, "update-ref", "refs/remotes/origin/main", self.outside)
        self.assertNotEqual(self.verify("v2.0.0").returncode, 0)
        self.assertEqual(self.git(self.work, "rev-parse", "origin/main").stdout.strip(), self.main)

    def test_main_is_fetched_again_instead_of_trusting_checkout_ref(self):
        self.git(self.work, "checkout", "--detach", "v2.0.0")
        self.git(self.origin, "update-ref", "refs/heads/main", self.outside)
        self.assertEqual(self.verify("v2.0.0").returncode, 0)

    def test_failed_fetch_rejects_even_with_previously_valid_main(self):
        self.git(self.work, "checkout", "--detach", "v1.0.0")
        self.git(self.work, "remote", "set-url", "origin", str(self.root / "missing.git"))
        self.assertNotEqual(self.verify("v1.0.0").returncode, 0)

    def test_missing_remote_main_rejects_even_with_stale_local_ref(self):
        self.git(self.work, "checkout", "--detach", "v1.0.0")
        self.git(self.origin, "update-ref", "-d", "refs/heads/main")
        self.assertNotEqual(self.verify("v1.0.0").returncode, 0)

    def test_mismatched_head_dirty_worktree_and_invalid_tags_are_rejected(self):
        self.assertNotEqual(self.verify("v1.0.0").returncode, 0)
        self.git(self.work, "checkout", "--detach", "v1.0.0")
        (self.work / "untracked").write_text("synthetic\n")
        self.assertNotEqual(self.verify("v1.0.0").returncode, 0)
        (self.work / "untracked").unlink()
        for tag in ("v01.0.0", "v1.0.0-next", "main", ""):
            with self.subTest(tag=tag):
                self.assertNotEqual(self.verify(tag).returncode, 0)


class ReleaseConfigurationTests(unittest.TestCase):
    def test_oss_channel_marker_and_workflow_binding(self):
        config = (ROOT / ".goreleaser.yaml").read_text()
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        main = (ROOT / "cmd/codex-switch/main.go").read_text()
        self.assertNotIn(".IsRelease", config)
        self.assertIn('main.buildChannel={{ envOrDefault "CODEX_SWITCH_BUILD_CHANNEL" "development" }}', config)
        self.assertIn("main.releaseRepository=hudrucan/codex-switch", config)
        self.assertIn('var buildChannel = "development"', main)
        self.assertIn("if: github.repository == 'hudrucan/codex-switch'", workflow)
        self.assertIn('bash scripts/verify-release-source.sh "$GITHUB_REF_NAME"', workflow)
        self.assertIn("fetch-depth: 0", workflow)
        self.assertEqual(workflow.count("CODEX_SWITCH_BUILD_CHANNEL:"), 1)
        action = workflow.split("- uses: goreleaser/goreleaser-action@", 1)[1].split("- uses:", 1)[0]
        self.assertIn("distribution: goreleaser\n", action)
        self.assertIn("CODEX_SWITCH_BUILD_CHANNEL: release", action)


if __name__ == "__main__":
    unittest.main()
