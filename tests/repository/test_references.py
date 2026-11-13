"""Check real local links and supported deployment entry points after cleanup."""
from pathlib import Path
import re
import unittest
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parents[2]


class RepositoryReferencesTest(unittest.TestCase):
    def test_frontend_changes_trigger_openwrt_validation(self):
        workflow = (ROOT / ".github/workflows/openwrt.yml").read_text()
        pull_request = workflow.split("  pull_request:", 1)[1].split("  workflow_dispatch:", 1)[0]
        patterns = re.findall(r"'([^']+)'", pull_request)
        # GitHub requires ** to cover nested component, page and test paths.
        self.assertIn("apps/web/**", patterns)

    def test_maintained_markdown_links_resolve(self):
        documents = [ROOT / name for name in ("README.md", "CONTRIBUTING.md", "docs/build.md", "docs/getting-started.md")]
        for path in documents:
            for target in re.findall(r"!?\[[^\]]*\]\(([^)]+)\)", path.read_text(encoding="utf-8")):
                url = urlsplit(target)
                if url.scheme or url.netloc or not url.path:
                    continue
                with self.subTest(document=path.name, target=target):
                    self.assertTrue((path.parent / unquote(url.path)).is_file())

    def test_systemd_and_desktop_binary_entry_remain_compatible(self):
        self.assertIn("ExecStart=/usr/bin/daed run -c /etc/daed/", (ROOT / "install/daed.service").read_text())
        self.assertIn('Exec=xdg-open "http://127.0.0.1:2023"', (ROOT / "install/daed.desktop").read_text())
        self.assertTrue((ROOT / "install/icons/256x256.png").is_file())
