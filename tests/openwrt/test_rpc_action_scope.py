"""Exercise RPC receipts with temporary SQLite/logs and mocked UCI/procd.

Only fixed executable/lock/log paths are relocated. No router files are accessed.
"""
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ServiceReceiptTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = root = Path(temporary.name)
        codec = root / "fixture_codec.py"
        codec.write_text('''import json, sys
from pathlib import Path
p = Path(sys.argv[1])
mode = sys.argv[2]
if mode == 'init': p.write_text('{}')
elif mode == 'dump': print(p.read_text())
elif mode == 'get':
    value = json.loads(p.read_text()).get(sys.argv[3])
    if value is None: sys.exit(1)
    print(value)
else:
    data = json.loads(p.read_text())
    data[sys.argv[3]] = sys.argv[4] if mode == 'string' else bool(int(sys.argv[4]))
    p.write_text(json.dumps(data))
''')
        jshn = root / "jshn.sh"
        jshn.write_text('''json_load() { printf '%s' "$1" > "$REQUEST_FILE"; }
json_get_var() {
 case "$1" in
 action) action=$(python3 "$CODEC" "$REQUEST_FILE" get "$2") ;;
 request_id) request_id=$(python3 "$CODEC" "$REQUEST_FILE" get "$2") ;;
 check_geodata) check_geodata=$(python3 "$CODEC" "$REQUEST_FILE" get "$2") ;;
 *) return 1 ;;
 esac
}
json_init() { python3 "$CODEC" "$JSON_FILE" init; }
json_add_boolean() { python3 "$CODEC" "$JSON_FILE" boolean "$1" "$2"; }
json_add_string() { python3 "$CODEC" "$JSON_FILE" string "$1" "$2"; }
json_dump() { python3 "$CODEC" "$JSON_FILE" dump; }
''')
        for name, body in {
            "check": "exit 0",
            "uci": 'printf "uci %s\\n" "$*" >> "$CALLS"',
            "service": 'printf "service %s\\n" "$*" >> "$CALLS"; exit "$RESULT"',
            "apply": '''printf "maintenance %s\\n" "$*" >> "$CALLS"
case "$2" in
 config_apply) printf '%s\\n' '{"success":true,"job":{"id":"fixture-job","action":"config_apply","state":"queued"}}' ;;
 status) printf '%s\\n' '{"success":true,"job":{"id":"fixture-job","action":"config_apply","state":"error","code":"configuration_fixed_group_members"}}' ;;
 *) exit 99 ;;
esac''',
        }.items():
            path = root / name
            path.write_text("#!/bin/sh\n" + body + "\n")
            path.chmod(0o700)
        self.script = (ROOT / "openwrt/files/rpcd-daed").read_text()
        self.replacements = {
            "/usr/share/libubox/jshn.sh": str(jshn),
            "/usr/libexec/daed/check": str(root / "check"),
            "/etc/init.d/daed": str(root / "service"),
            "/usr/libexec/daed/luci-proxy": str(root / "apply"),
            "/var/lock": str(root / "lock"),
            "/var/log/daed/daed.log": str(root / "core.log"),
        }
        self.env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                        CODEC=str(codec), JSON_FILE=str(root / "receipt.json"),
                        REQUEST_FILE=str(root / "request.json"), RESULT="0", CALLS=str(root / "calls"))
        self.startup_log = 'fixture: Health check outbounds: [direct]\nfixture: Ready\n'
        (root / "core.log").write_text(self.startup_log)
        with sqlite3.connect(root / "wing.db") as database:
            database.executescript('''CREATE TABLE group_nodes(group_id INTEGER, node_id INTEGER);
INSERT INTO group_nodes VALUES (1,16);
CREATE TABLE systems(running INTEGER, running_group_version_sum INTEGER);
INSERT INTO systems VALUES (0,4);''')

    def call(self, method, arguments=None):
        script = self.script
        for original, replacement in self.replacements.items():
            script = script.replace(original, replacement)
        executable = self.root / "rpcd"
        executable.write_text(script)
        completed = subprocess.run(["sh", str(executable), "call", method],
                                   input=json.dumps(arguments or {}) + "\n", text=True,
                                   capture_output=True, env=self.env, timeout=5)
        self.assertEqual(completed.returncode, 0, completed.stderr)
        return json.loads(completed.stdout)

    def test_start_receipt_does_not_claim_configuration_was_applied(self):
        for action, result in (("start", 0), ("restart", 0), ("start", 1)):
            with self.subTest(action=action, result=result):
                (self.root / "calls").write_text("")
                self.env["RESULT"] = str(result)
                receipt = self.call("action", {"action": action})
                self.assertEqual(receipt["success"], result == 0)
                self.assertEqual(receipt["scope"], "service")
                self.assertNotIn("applied", receipt)
                if result == 0:
                    self.assertIn("配置状态", receipt["message"])
                self.assertEqual((self.root / "calls").read_text().splitlines(), [
                    "uci set daed.main.enabled=1", "uci commit daed", "service " + action,
                ])

    def test_apply_on_service_entry_is_rejected_before_backend_or_core(self):
        database_before = (self.root / "wing.db").read_bytes()
        for action in ("apply", "config_apply", "https://fixture.invalid/?token=secret"):
            with self.subTest(action=action):
                receipt = self.call("action", {"action": action})
                self.assertFalse(receipt["success"])
                self.assertFalse((self.root / "calls").exists(), "invalid action reached a service/backend")
                self.assertEqual(self.call("logs")["logs"], self.startup_log.rstrip())
                self.assertEqual((self.root / "wing.db").read_bytes(), database_before)
                self.assertEqual(receipt.get("code"), "invalid_action")
                self.assertEqual(receipt.get("scope"), "service")
                self.assertIn("config_apply", receipt["message"])
                self.assertNotIn("fixture.invalid", json.dumps(receipt))
                self.assertNotIn("secret", json.dumps(receipt))

    def test_configuration_uses_maintenance_job_and_reads_its_result(self):
        request_id = "a" * 24
        receipt = self.call("maintain", {"action": "config_apply", "request_id": request_id})
        self.assertTrue(receipt["success"])
        self.assertEqual(receipt["job"]["state"], "queued")
        result = self.call("maintenance")
        self.assertEqual(result["job"]["id"], receipt["job"]["id"])
        self.assertEqual(result["job"]["state"], "error")
        self.assertEqual(result["job"]["code"], "configuration_fixed_group_members")
        self.assertEqual((self.root / "calls").read_text().splitlines(), [
            "maintenance --maintenance config_apply --maintenance-job " + request_id,
            "maintenance --maintenance status",
        ])
