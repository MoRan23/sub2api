import gzip
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("detector_update", Path(__file__).with_name("update.py"))
updater = importlib.util.module_from_spec(spec)
spec.loader.exec_module(updater)


def config(context="/previous"):
    return {
        "services": {"lm-detector": {"build": {"context": context}, "image": "detector:old", "networks": {"private": {}}}},
        "networks": {"private": {"external": True, "name": "sub2api-network"}},
    }


class UpdateTests(unittest.TestCase):
    def test_ref_is_resolved_once_to_fixed_commit(self):
        with patch.object(updater, "download", return_value=json.dumps({"sha": "a" * 40}).encode()) as request:
            self.assertEqual(updater.resolve_revision("release/v2"), "a" * 40)
            self.assertTrue(request.call_args.args[0].endswith("/commits/release%2Fv2"))
        with patch.object(updater, "download", return_value=b'{"sha":"main"}'):
            with self.assertRaises(ValueError):
                updater.resolve_revision("main")

    def test_rejects_other_services_ports_and_nonexternal_networks(self):
        self.assertEqual(updater.validate_compose(config()), "sub2api-network")
        for modify in (
            lambda c: c["services"].update({"sub2api": {}}),
            lambda c: c["services"]["lm-detector"].update({"ports": ["8080:8080"]}),
            lambda c: c["networks"]["private"].update({"external": False}),
        ):
            candidate = config()
            modify(candidate)
            with self.assertRaises(ValueError):
                updater.validate_compose(candidate)

    def test_only_official_shared_code_and_data_are_vendored_together(self):
        files = {p: (p + "\n").encode() for p in updater.DATA | {"LICENSE", "shared/shared-detector.ts", "shared/challenge-browser.js"}}
        tree = {"tree": [{"path": p, "type": "blob", "mode": "100644"} for p in files] + [{"path": "research/large.bin", "type": "blob", "mode": "100644"}]}
        with tempfile.TemporaryDirectory() as tmp:
            template = Path(tmp) / "template"
            template.mkdir()
            for name in updater.WRAPPER:
                (template / name).write_text("wrapper unchanged")
            target = Path(tmp) / "new"

            def fetch(url, *args):
                if "/git/trees/" in url:
                    return json.dumps(tree).encode()
                return files[url.split("/" + "a" * 40 + "/", 1)[1]]

            with patch.object(updater, "download", side_effect=fetch):
                updater.vendor_release(template, target, "a" * 40)
            manifest = json.loads((target / "upstream.json").read_text())
            self.assertEqual(set(manifest["files"]), set(files))
            self.assertEqual(manifest["revision"], "a" * 40)
            for path in updater.DATA:
                self.assertEqual(gzip.decompress((target / "vendor" / (path + ".gz")).read_bytes()), files[path])
            self.assertEqual((target / "server.ts").read_text(), "wrapper unchanged")
        for extra in ({"path": "shared/../bad", "type": "blob", "mode": "100644"}, {"path": "shared/link", "type": "blob", "mode": "120000"}):
            with self.assertRaises(ValueError):
                updater.source_paths({"tree": tree["tree"] + [extra]})
        with self.assertRaises(ValueError):
            updater.source_paths({**tree, "truncated": True})

    def test_rejects_incompatible_protocol_and_missing_calibration(self):
        info = {"provider": "lm_fingerprint_detector", "protocol": 1, "algorithm": "shared-detector-v1", "revision": "a" * 40, "bank_built_at": "2026-10-03", "ranker_sha256": "b" * 64, "reference_sha256": "c" * 64, "calibration_sha256": "d" * 64}
        self.assertEqual(updater.validate_info(info, "a" * 40), ":".join(["a" * 40, "b" * 64, "c" * 64, "d" * 64]))
        for change in ({"protocol": 2}, {"algorithm": "new-algorithm"}, {"calibration_sha256": ""}, {"revision": "e" * 40}):
            with self.assertRaises(ValueError):
                updater.validate_info({**info, **change}, "a" * 40)

    def test_failed_switch_restores_original_compose_and_only_detector(self):
        with tempfile.TemporaryDirectory() as tmp:
            deploy = updater.Deployment(Path(tmp), "sub2api")
            original = "# preserve this file on failure\n" + updater.serialize(config())
            deploy.compose.write_text(original)
            deploy.up = Mock(side_effect=[RuntimeError("candidate failed"), None])
            deploy.health = Mock()
            with self.assertRaisesRegex(RuntimeError, "candidate failed"):
                deploy.switch(config("/new"), "new", config(), "old")
            self.assertEqual(deploy.compose.read_text(), original)
            self.assertEqual(deploy.up.call_count, 2)
            deploy.health.assert_called_once_with("lm-detector", "old")
            self.assertFalse((Path(tmp) / "rollback.json").exists())
            self.assertEqual(len(list((Path(tmp) / "backups").glob("*.json"))), 1)
            with patch.object(updater, "run") as run:
                updater.Deployment.up(deploy)
            command = run.call_args.args[0]
            self.assertEqual(command[-1], "lm-detector")
            self.assertIn("--no-deps", command)
            self.assertIn("--no-build", command)

    def test_success_saves_previous_release_for_rollback(self):
        with tempfile.TemporaryDirectory() as tmp:
            deploy = updater.Deployment(Path(tmp), "sub2api")
            deploy.compose.write_text(updater.serialize(config()))
            deploy.up = Mock()
            deploy.health = Mock()
            deploy.switch(config("/new"), "new", config(), "old")
            self.assertEqual(json.loads(deploy.compose.read_text()), config("/new"))
            self.assertEqual(json.loads((Path(tmp) / "rollback.json").read_text()), config())
            deploy.health.assert_called_once_with("lm-detector", "new")


if __name__ == "__main__":
    unittest.main()
