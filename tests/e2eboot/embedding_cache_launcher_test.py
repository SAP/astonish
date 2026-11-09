import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('cache_launcher', Path(__file__).with_name('local_bot_smoke_launcher.py'))
launcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(launcher)

class EmbeddingCacheTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'fresh'
        self.root.mkdir()
        self.source = Path(self.tmp.name) / 'cache'
        self.source.mkdir()
        self.hashes = {}
        for name in launcher.EMBEDDING_HASHES:
            data = ('fixture ' + name).encode()
            (self.source / name).write_bytes(data)
            self.hashes[name] = hashlib.sha256(data).hexdigest()
        (self.source / 'credentials.json').write_text('DO NOT COPY')
        self.mock = patch.object(launcher, 'EMBEDDING_HASHES', self.hashes)
        self.mock.start()
        self.addCleanup(self.mock.stop)
    def stage(self):
        launcher.stage_embedding_cache(self.root, str(self.source))
    def test_only_verified_assets_copied_without_source_mutation(self):
        before = {p.name: p.read_bytes() for p in self.source.iterdir()}
        self.stage()
        destination = self.root / 'config/astonish/models' / launcher.EMBEDDING_MODEL
        self.assertEqual(set(self.hashes), {p.name for p in destination.iterdir()})
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.source.iterdir()})
        for name in self.hashes:
            self.assertNotEqual((self.source/name).stat().st_ino, (destination/name).stat().st_ino)
        self.assertTrue((self.root/'embedding-cache-receipt.json').is_file())
    def test_each_bad_hash_rejected(self):
        for name in self.hashes:
            with self.subTest(name=name):
                src=self.source/name; original=src.read_bytes();src.write_bytes(b'corrupt')
                with self.assertRaisesRegex(RuntimeError,'hash mismatch'): self.stage()
                src.write_bytes(original)
                self.assertFalse((self.root/'embedding-cache-receipt.json').exists())
    def test_symlink_rejected(self):
        src=self.source/'model.onnx';src.unlink();src.symlink_to(self.source/'config.json')
        with self.assertRaisesRegex(RuntimeError,'symlink'): self.stage()
    def test_no_overwrite(self):
        self.stage()
        with self.assertRaisesRegex(RuntimeError,'overwrite'): self.stage()
    def test_relative_rejected(self):
        with self.assertRaises(RuntimeError): launcher.stage_embedding_cache(self.root, 'relative')
