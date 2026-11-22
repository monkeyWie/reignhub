import contextlib
import io
import os
from pathlib import Path
import tempfile
import unittest
from types import SimpleNamespace
from unittest.mock import patch

import release


class ReleaseContractTests(unittest.TestCase):
    def test_metadata_and_tag_validation(self):
        with patch.dict(os.environ, {'GITHUB_REF': 'refs/tags/v1.2.3-rc.1', 'GITHUB_RUN_NUMBER': '21', 'GITHUB_OUTPUT': ''}):
            out = io.StringIO()
            with contextlib.redirect_stdout(out):
                release.metadata(None)
            self.assertIn('version=1.2.3-rc.1\nbuild_number=21\n', out.getvalue())
        for ref in ('refs/tags/vnext', 'refs/tags/v1.2', 'refs/tags/1.2.3', 'refs/tags/v1.2.3/extra'):
            with patch.dict(os.environ, {'GITHUB_REF': ref, 'GITHUB_OUTPUT': ''}), self.assertRaises(SystemExit):
                release.metadata(None)

    def test_publication_rejects_missing_extra_and_corrupted_assets(self):
        with tempfile.TemporaryDirectory() as tmp:
            output = Path(tmp)
            v = '1.2.3'
            names = release.desktop_assets(v)
            names += [f'reignhub-{v}-ios-unsigned.ipa', f'reignhub-{v}-android-arm64-debug.apk']
            names += release.server_assets(v)
            args = SimpleNamespace(version=v, output=tmp)
            for name in names:
                path = output / name
                path.write_bytes(b'fixture artifact')
                release.checksum(path)
            release.verify(args)
            self.assertEqual(len((output / 'SHA256SUMS').read_text().splitlines()), 18)
            path.write_bytes(b'corrupted artifact')
            with self.assertRaises(SystemExit):
                release.verify(args)
            release.checksum(path)
            extra = output / 'unexpected-secret.pem'
            extra.write_text('must never publish this file')
            with self.assertRaises(SystemExit):
                release.verify(args)
            extra.unlink()
            path.unlink()
            with self.assertRaises(SystemExit):
                release.verify(args)

    def test_collects_native_installers_without_repacking_or_publishing_bundles(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / 'build/releases'
            source.mkdir(parents=True)
            for name in release.desktop_assets('1.2.3'):
                (source / name).write_bytes(b'native installer fixture: ' + name.encode())
            (source / 'reignhub-1.2.3-windows-x64.zip').write_bytes(b'portable archive')
            args = SimpleNamespace(version='1.2.3', source=str(source.parent), output=str(root / 'out'), targets=','.join(release.DESKTOP))
            release.collect(args)
            names = {p.name for p in (root / 'out').iterdir() if p.suffix != '.sha256'}
            self.assertEqual(names, set(release.desktop_assets('1.2.3')))
            for name in names:
                self.assertEqual((root / 'out' / name).read_bytes(), (source / name).read_bytes())
            (source / 'reignhub-1.2.3-windows-x64-setup.exe').unlink()
            with self.assertRaisesRegex(SystemExit, 'Missing native installer'):
                release.collect(args)


if __name__ == '__main__':
    unittest.main()
