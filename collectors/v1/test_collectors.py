import importlib.util
import io
import json
import tempfile
import unittest
import zipfile
from pathlib import Path


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).parent / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


access = load("access")
inventory = load("inventory")


class AccessTests(unittest.TestCase):
    def setUp(self):
        self.policy = {"version": "1", "revision": "approved-policy", "actions": {"download": {"resource_type": "attachment", "require_authentication": True, "allow": [{"kind": "resource_owner"}, {"kind": "explicit_share"}]}}}
        self.event = {"action": "download", "resource_type": "attachment", "policy_revision": "approved-policy", "integrity": "authenticated_report", "authenticated": True, "at": "2026-10-31T12:00:00Z", "facts_valid_from": "2026-10-31T11:00:00Z", "facts_valid_until": "2026-10-31T13:00:00Z", "facts": {"resource_owner": False, "explicit_share": False}, "protected_effect": "demonstrated_success"}

    def test_deny_requires_demonstrated_effect(self):
        self.assertEqual(access.evaluate(self.policy, self.event)["classification"], "unauthorized_access_candidate")
        self.event["protected_effect"] = "http_200"
        self.assertEqual(access.evaluate(self.policy, self.event)["classification"], "unknown")

    def test_unknown_history_and_known_allow(self):
        self.event["facts"]["resource_owner"] = None
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "unknown")
        self.event["facts"]["explicit_share"] = True
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "allow")
        self.event["facts_valid_until"] = "2026-10-31T12:00:00Z"
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "unknown")

    def test_tenant_role_scope_and_empty_roles(self):
        self.policy["actions"]["download"]["allow"] = [{"kind": "role", "role": "admin", "scope": "tenant"}]
        self.event["tenant_id"] = "tenant-a"
        self.event["facts"]["roles"] = [{"role": "admin", "scope": "tenant", "tenant_id": "tenant-b"}]
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "deny")
        self.event["facts"]["roles"] = None
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "unknown")
        self.event["facts"]["roles"] = []
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "deny")

    def test_anonymous_and_unknown_action(self):
        self.event["authenticated"] = False
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "deny")
        self.event["action"] = "undeclared"
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "unknown")

    def test_missing_tenant_or_policy_revision_stays_unknown(self):
        self.policy["actions"]["download"]["allow"] = [{"kind": "role", "role": "admin", "scope": "tenant"}]
        self.event["facts"]["roles"] = [{"role": "admin", "scope": "tenant", "tenant_id": "tenant-a"}]
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "unknown")
        self.policy.pop("revision")
        self.event.pop("policy_revision")
        self.assertEqual(access.evaluate(self.policy, self.event)["decision"], "unknown")
        self.assertEqual(access.evaluate(self.policy, ["unsupported"])["decision"], "unknown")


class InventoryTests(unittest.TestCase):
    def test_xml_control_files_archive_hazards_and_partial_results(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "document.svg").write_text('<?xml version="1.0"?><svg/>')
            (root / ".user.ini").write_text("auto_prepend_file=payload.php")
            (root / "payload.php").write_text("<?php eval('example');")
            (root / "large.php").write_bytes(b"x" * (1024 * 1024 + 1))
            with zipfile.ZipFile(root / "archive.zip", "w") as archive:
                archive.writestr("../../outside.php", "example")
                archive.writestr("word/vbaProject.bin", "example")
            items, omissions, boundaries = inventory.collect(root, "php")
            by_path = {item["path"]: item for item in items if "path" in item}
            self.assertNotIn("document.svg", by_path)
            self.assertIn("handler_or_configuration_override", by_path[".user.ini"]["indicators"])
            self.assertIn("php_tag", by_path["payload.php"]["indicators"])
            self.assertTrue(any("archive_path_escape" in item.get("indicators", []) for item in items))
            self.assertTrue(any("document_macro" in item.get("indicators", []) for item in items))
            self.assertTrue(omissions)


if __name__ == "__main__":
    unittest.main()
