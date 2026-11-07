"""Exercise service receipts in a temporary filesystem with mocked UCI/procd.

Only fixed executable/lock paths are relocated. No router files are accessed.
"""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ServiceReceiptTests(unittest.TestCase):
    def test_start_receipt_does_not_claim_configuration_was_applied(self):
        for action, result in (("start", 0), ("restart", 0), ("start", 1)):
            with self.subTest(action=action, result=result), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                codec = root / "fixture_codec.py"
                codec.write_text('''import json, sys
from pathlib import Path
p = Path(sys.argv[1])
mode = sys.argv[2]
if mode == 'init': p.write_text('{}')
elif mode == 'dump': print(p.read_text())
else:
    data = json.loads(p.read_text())
    data[sys.argv[3]] = sys.argv[4] if mode == 'string' else bool(int(sys.argv[4]))
    p.write_text(json.dumps(data))
''')
                jshn = root / "jshn.sh"
                jshn.write_text('''json_load() { :; }
json_get_var() { action="$FIXTURE_ACTION"; }
json_init() { python3 "$CODEC" "$JSON_FILE" init; }
json_add_boolean() { python3 "$CODEC" "$JSON_FILE" boolean "$1" "$2"; }
json_add_string() { python3 "$CODEC" "$JSON_FILE" string "$1" "$2"; }
json_dump() { python3 "$CODEC" "$JSON_FILE" dump; }
''')
                replacements = {
                    "/usr/share/libubox/jshn.sh": str(jshn),
                    "/usr/libexec/daed/check": str(root / "check"),
                    "/etc/init.d/daed": str(root / "service"),
                    "/usr/libexec/daed/luci-proxy": str(root / "apply"),
                    "/var/lock": str(root / "lock"),
                }
                for name, body in {
                    "check": "exit 0",
                    "uci": 'printf "uci %s\\n" "$*" >> "$CALLS"',
                    "service": 'printf "service %s\\n" "$*" >> "$CALLS"; exit "$RESULT"',
                    "apply": 'echo apply >> "$CALLS"; exit 99',
                }.items():
                    path = root / name
                    path.write_text("#!/bin/sh\n" + body + "\n")
                    path.chmod(0o700)
                script = (ROOT / "openwrt/files/rpcd-daed").read_text()
                for original, replacement in replacements.items():
                    script = script.replace(original, replacement)
                executable = root / "rpcd"
                executable.write_text(script)
                env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                           CODEC=str(codec), JSON_FILE=str(root / "receipt.json"),
                           FIXTURE_ACTION=action, RESULT=str(result), CALLS=str(root / "calls"))
                completed = subprocess.run(["sh", str(executable), "call", "action"],
                                           input=json.dumps({"action": action}) + "\n",
                                           text=True, capture_output=True, env=env, timeout=5)
                self.assertEqual(completed.returncode, 0, completed.stderr)
                receipt = json.loads(completed.stdout)
                self.assertEqual(receipt["success"], result == 0)
                self.assertEqual(receipt["scope"], "service")
                self.assertNotIn("applied", receipt)
                if result == 0:
                    self.assertIn("配置状态", receipt["message"])
                self.assertEqual((root / "calls").read_text().splitlines(), [
                    "uci set daed.main.enabled=1", "uci commit daed", "service " + action,
                ])
