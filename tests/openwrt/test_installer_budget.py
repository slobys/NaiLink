"""Download deadline and dependency recovery with real BusyBox ash."""
import unittest
import shutil

import test_router


class InstallerBudgetTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        test_router.RouterTests.setUpClass()

    def setUp(self):
        self.router = test_router.RouterTests()
        self.router.setUp()
        self.addCleanup(self.router.tearDown)

    def management_wait(self, ready_at=None, request_seconds=0, backward_clock=False):
        source = (test_router.OPENWRT/'install.sh').read_text().split('\ninstall_latest() {')[0]
        self.router.write('/fixtures/download-functions', source)
        self.router.write('/proc/uptime', '1000.0 0.0\n')
        self.router.write('/tmp/daemon-running', '')
        self.router.script('/bin/date', 'echo 1')
        self.router.script('/bin/sleep', '''read -r seconds rest </proc/uptime
echo "$((${seconds%%.*} + 1)).0 0.0" >/proc/uptime''')
        self.router.script('/bin/uclient-fetch', '''read -r seconds rest </proc/uptime
seconds=${seconds%%.*}
echo probe >>/tmp/http-probes
echo "$((seconds + REQUEST_SECONDS)).0 0.0" >/proc/uptime
[ "$seconds" -ge READY_AT ]'''.replace('REQUEST_SECONDS', str(request_seconds)).replace('READY_AT', str(ready_at or 100000)))
        if backward_clock:
            self.router.script('/bin/sleep', 'echo "1.0 0.0" >/proc/uptime')
        return self.router.run_router('/bin/sh', '-c', '. /fixtures/download-functions; wait_management_http 127.0.0.1:2023')

    def test_slow_management_startup_is_not_prematurely_rolled_back(self):
        result = self.management_wait(ready_at=1060)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertGreater(len((self.router.root/'tmp/http-probes').read_text().splitlines()), 20)

    def test_management_http_timeouts_share_one_monotonic_budget(self):
        result = self.management_wait(request_seconds=2)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.router.root/'proc/uptime').read_text().split()[0], '1120.0')
        self.assertEqual(len((self.router.root/'tmp/http-probes').read_text().splitlines()), 40)

    def test_management_wait_is_bounded_even_if_clock_goes_backwards(self):
        result = self.management_wait(backward_clock=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(len((self.router.root/'tmp/http-probes').read_text().splitlines()), 120)

    def test_retries_share_deadline_and_clean_partial_downloads(self):
        source = (test_router.OPENWRT/'install.sh').read_text().split('\ninstall_latest() {')[0]
        self.router.write('/fixtures/download-functions', source)
        self.router.write('/tmp/clock', '1000\n')
        self.router.script('/bin/date', 'if [ "$*" = +%s ]; then cat /tmp/clock; else /bin/busybox date "$@"; fi')
        body = '''output=''; budget=''
while [ "$#" -gt 0 ]; do
 case "$1" in
 --output|-O) output=$2; shift 2 ;;
 --max-time|-T) budget=$2; shift 2 ;;
 *) shift ;;
 esac
done
echo "$budget" >> /tmp/attempt-budgets
elapsed=40; [ "$budget" -ge 40 ] || elapsed=$budget
echo "$(($(cat /tmp/clock) + elapsed))" > /tmp/clock
printf partial > "$output"
exit 28'''
        for client in ('curl', 'uclient-fetch'):
            self.router.script('/bin/'+client, body)
        result = self.router.run_router('/bin/sh', '-c', '. /fixtures/download-functions; download_https /tmp/transfer https://github.com/example 100')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual((self.router.root/'tmp/attempt-budgets').read_text().splitlines(), ['100', '60', '20'])
        self.assertEqual((self.router.root/'tmp/clock').read_text().strip(), '1100')
        self.assertFalse((self.router.root/'tmp/transfer').exists())

    def test_apk_satisfied_dependencies_do_not_refresh_unavailable_feeds(self):
        self.router.use_apk()
        self.router.managed()
        self.router.write('/tmp/feed-fail', '')
        result = self.router.install('--install')
        self.assertEqual(result.returncode, 0, result.stderr+result.stdout)
        calls = (self.router.root/'tmp/pm-calls').read_text().splitlines()
        self.assertNotIn('update', calls)
        self.assertTrue(any(line.startswith('add --no-network --allow-untrusted ') for line in calls))
        self.assertTrue((self.router.root/'tmp/daemon-running').exists())

    def test_wall_clock_correction_does_not_reset_monotonic_budget(self):
        source=(test_router.OPENWRT/'install.sh').read_text().split('\ninstall_latest() {')[0]
        self.router.write('/fixtures/download-functions',source)
        self.router.write('/proc/uptime','1234.56 0.00\n')
        self.router.script('/bin/date','echo 999999999')
        result=self.router.run_router('/bin/sh','-c','. /fixtures/download-functions; dl_deadline=1300; dl_timeout=180; download_remaining; echo "$dl_remaining"')
        self.assertEqual(result.returncode,0,result.stderr)
        self.assertEqual(result.stdout.strip(),'66')

    def test_missing_apk_dependencies_refresh_feeds_before_stopping(self):
        self.router.use_apk()
        self.router.managed()
        self.router.write('/tmp/offline-preflight-fail', '')
        result = self.router.install('--install')
        self.assertEqual(result.returncode, 0, result.stderr+result.stdout)
        calls = (self.router.root/'tmp/pm-calls').read_text().splitlines()
        self.assertIn('update', calls)
        self.assertTrue(any(line.startswith('add --simulate ') for line in calls))
        self.assertTrue((self.router.root/'tmp/daemon-running').exists())

    def test_failed_required_feed_refresh_preserves_data_and_running_service(self):
        self.router.use_apk()
        self.router.managed()
        self.router.write('/tmp/offline-preflight-fail', '')
        self.router.write('/tmp/feed-fail', '')
        database=self.router.write('/etc/daed/wing.db', 'keep nodes and configuration')
        config=(self.router.root/'etc/config/daed').read_bytes()
        result = self.router.install('--install')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Firmware package feed refresh failed', result.stderr)
        self.assertTrue((self.router.root/'tmp/daemon-running').exists())
        self.assertEqual(database.read_text(), 'keep nodes and configuration')
        self.assertEqual((self.router.root/'etc/config/daed').read_bytes(), config)
        self.assertFalse((self.router.root/'root/daed-backups').exists())

    def test_preparation_budget_is_checked_again_before_stopping_service(self):
        self.router.managed()
        shutil.copytree(self.router.root/'assets', self.router.root/'old-assets')
        self.router.assets(version='0.2.0')
        shutil.copytree(self.router.root/'old-assets', self.router.root/'assets/rollback')
        self.router.script('/bin/date', '''if [ "$*" = +%s ]; then
 if [ -f /tmp/preflight-complete ]; then echo 2000; else echo 1000; fi
else /bin/busybox date "$@"; fi''')
        manager=self.router.root/'bin/opkg'
        manager.write_text(manager.read_text().replace('update) [ ! -e /tmp/feed-fail ] ;;', 'update) touch /tmp/preflight-complete ;;'))
        database=self.router.write('/etc/daed/wing.db', 'keep original database')
        result=self.router.install('--install', '--update-only')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Update preparation timed out', result.stderr)
        self.assertTrue((self.router.root/'tmp/daemon-running').exists())
        self.assertEqual(database.read_text(), 'keep original database')
        self.assertFalse((self.router.root/'root/daed-backups').exists())

    def test_invalid_preparation_budget_never_enters_installation(self):
        for value in ('0', '901', '001', '-1', 'invalid'):
            with self.subTest(value=value):
                result=self.router.install('--prepare-timeout',value)
                self.assertNotEqual(result.returncode,0)
                self.assertIn('Invalid preparation timeout',result.stderr)
                self.assertFalse((self.router.root/'var/lock/daed-install.lock').exists())
