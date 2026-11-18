"""Validate the rollback flag and metadata using the real rootfs opkg."""
import io
import tarfile
import unittest

import test_router


def archive(files):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode='w:gz') as stream:
        for name, data in files.items():
            entry = tarfile.TarInfo(name); entry.size = len(data); entry.mode = 0o644
            stream.addfile(entry, io.BytesIO(data))
    return buffer.getvalue()


class NativeOpkgRollbackTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        test_router.RouterTests.setUpClass()

    def test_verified_local_older_package_restores_version_and_payload(self):
        case = test_router.RouterTests(); case.setUp()
        try:
            binary = case.base/'bin/opkg'
            if not binary.is_file(): self.skipTest('This rootfs has no native opkg')
            case.write('/bin/opkg', binary.read_bytes(), True)
            (case.root/'var/lock').mkdir(parents=True,exist_ok=True)
            paths = []
            for version in ('0.1.0-1','0.2.0-1'):
                control = ('Package: nailink-recovery-test\nVersion: '+version+'\nArchitecture: x86_64\nInstalled-Size: 4097\nMaintainer: Test\nDescription: Isolated rollback test\n').encode()
                package = archive({'debian-binary':b'2.0\n','control.tar.gz':archive({'./control':control}),'data.tar.gz':archive({'./usr/share/nailink-recovery-test':version.encode()})})
                path = '/fixtures/'+version+'.ipk'; case.write(path,package); paths.append(path)
            source = (test_router.OPENWRT/'install.sh').read_text()
            sizing = source[source.index('package_payload_kib()'):source.index('check_upgrade_space()')]
            case.write('/fixtures/package-sizing', sizing)
            result = case.run_router('/bin/sh', '-c', '. /fixtures/package-sizing; format=ipk; package_payload_kib "$1"', 'sizing', paths[1])
            self.assertEqual((result.returncode, result.stdout.strip()), (0, '5'), result.stderr)
            result = case.run_router('opkg','install',paths[1])
            self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            result = case.run_router('opkg','--force-downgrade','install',paths[0])
            self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertEqual((case.root/'usr/share/nailink-recovery-test').read_text(),'0.1.0-1')
            self.assertIn('Version: 0.1.0-1',case.run_router('opkg','status','nailink-recovery-test').stdout)
        finally:
            case.tearDown()
