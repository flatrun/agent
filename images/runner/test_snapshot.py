import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest

HELPER = Path(sys.argv.pop(1)).resolve()


class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='flatrun-snapshot-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.cache = self.root / 'cache'
        self.cache.mkdir()
        self.env = {**os.environ, 'FLATRUN_SNAPSHOT_CACHE': str(self.cache)}
        self.command = [sys.executable, '-c', 'from pathlib import Path; Path("out").mkdir(); Path("out/value").write_text("ready")']

    def run_helper(self, name, key='key', command=None, path='out'):
        cwd = self.root / name
        cwd.mkdir(exist_ok=True)
        return subprocess.run([sys.executable, str(HELPER), '--key', key, '--path', path, '--', *(command or self.command)], cwd=cwd, env=self.env, capture_output=True, text=True)

    def test_restore_and_isolation(self):
        first = self.run_helper('first')
        second = self.run_helper('second')
        other = self.run_helper('other', key='different')
        self.assertEqual((first.returncode, second.returncode, other.returncode), (0, 0, 0))
        self.assertIn('saved', first.stdout)
        self.assertIn('restored', second.stdout)
        self.assertIn('saved', other.stdout)
        self.assertEqual((self.root / 'second/out/value').read_text(), 'ready')

    def test_failed_setup_and_path_escape(self):
        failed = self.run_helper('failed', command=[sys.executable, '-c', 'raise SystemExit(7)'])
        bad = self.run_helper('bad', path='../outside')
        self.assertEqual(failed.returncode, 7)
        self.assertNotEqual(bad.returncode, 0)
        self.assertFalse(list((self.cache / 'snapshots').glob('*.tar')))

    def test_rejects_malicious_archive(self):
        self.assertEqual(self.run_helper('first').returncode, 0)
        archive = next((self.cache / 'snapshots').glob('*.tar'))
        with tarfile.open(archive, 'w') as tar:
            member = tarfile.TarInfo('../escaped')
            payload = b'bad'
            member.size = len(payload)
            tar.addfile(member, io.BytesIO(payload))
        result = self.run_helper('second')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'escaped').exists())

    def test_restores_ordinary_group_permissions(self):
        command = [sys.executable, '-c', 'from pathlib import Path; import os; p=Path("out"); p.mkdir(); os.chmod(p, 0o775); f=p/"value"; f.write_text("ready"); os.chmod(f, 0o664)']
        first = self.run_helper('first', command=command)
        second = self.run_helper('second', command=command)
        self.assertEqual((first.returncode, second.returncode), (0, 0), second.stderr)
        self.assertEqual((self.root / 'second/out').stat().st_mode & 0o777, 0o775)
        self.assertEqual((self.root / 'second/out/value').stat().st_mode & 0o777, 0o664)

    def test_runs_setup_when_cache_is_unavailable(self):
        self.env['FLATRUN_SNAPSHOT_CACHE'] = str(self.root / 'missing-cache')
        result = self.run_helper('uncached')
        self.assertEqual(result.returncode, 0)
        self.assertEqual((self.root / 'uncached/out/value').read_text(), 'ready')
        self.assertFalse((self.root / 'missing-cache').exists())

    def test_runs_setup_when_cache_directory_is_unusable(self):
        (self.cache / 'snapshots').write_text('not a directory')
        result = self.run_helper('uncached')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('unavailable', result.stderr)
        self.assertEqual((self.root / 'uncached/out/value').read_text(), 'ready')

    def test_successful_setup_survives_archive_failure(self):
        self.env['PATH'] = ''
        result = self.run_helper('uncached')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('not saved', result.stderr)
        self.assertEqual((self.root / 'uncached/out/value').read_text(), 'ready')

    def test_rejects_symlink_outside_restore_root(self):
        self.assertEqual(self.run_helper('first').returncode, 0)
        archive = next((self.cache / 'snapshots').glob('*.tar'))
        with tarfile.open(archive, 'w') as tar:
            folder = tarfile.TarInfo('out')
            folder.type = tarfile.DIRTYPE
            tar.addfile(folder)
            link = tarfile.TarInfo('out/link')
            link.type = tarfile.SYMTYPE
            link.linkname = '../../escaped'
            tar.addfile(link)
        result = self.run_helper('second')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'escaped').exists())

    def test_concurrent_jobs_only_run_setup_once(self):
        counter = self.root / 'counter'
        code = f'import time; from pathlib import Path; p=Path({str(counter)!r}); p.open("a").write("x"); time.sleep(1); Path("out").mkdir(); Path("out/value").write_text("ready")'
        command = [sys.executable, '-c', code]
        processes = []
        for name in ('one', 'two'):
            cwd = self.root / name
            cwd.mkdir()
            processes.append(subprocess.Popen([sys.executable, str(HELPER), '--key', 'key', '--path', 'out', '--', *command], cwd=cwd, env=self.env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True))
        results = [p.communicate(timeout=20) + (p.returncode,) for p in processes]
        self.assertEqual([r[2] for r in results], [0, 0], results)
        self.assertEqual(counter.read_text(), 'x')
        self.assertEqual((self.root / 'one/out/value').read_text(), 'ready')
        self.assertEqual((self.root / 'two/out/value').read_text(), 'ready')


if __name__ == '__main__':
    unittest.main()
