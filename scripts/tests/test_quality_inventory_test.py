import importlib.util
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[1] / "test-quality-inventory.py"
spec = importlib.util.spec_from_file_location("quality_inventory", SCRIPT)
inventory = importlib.util.module_from_spec(spec)
spec.loader.exec_module(inventory)


class InventoryTest(unittest.TestCase):
    def test_nested_module_and_build_constraint_are_recorded(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "go.mod").write_text("module example.com/root\n")
            nested = root / "test" / "simulation"
            nested.mkdir(parents=True)
            (nested / "go.mod").write_text("module example.com/sim\n")
            (nested / "mainnet_test.go").write_text(
                "//go:build mainnet && !mocknet\n\npackage sim\n"
            )
            result = inventory.inventory(root)
            self.assertEqual(result["modules"], [".", "test/simulation"])
            self.assertEqual(
                result["build_constraints"],
                {"test/simulation/mainnet_test.go": "mainnet && !mocknet"},
            )
            self.assertEqual(
                result["test_file_counts_by_directory"], {"test/simulation": 1}
            )


if __name__ == "__main__":
    unittest.main()
