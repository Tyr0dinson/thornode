"""Check the effective XMR deployment settings after Compose inheritance."""

import json
import os
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[3]
TEST_TOKEN = "xmr-readiness-contract-test-token"
PROFILES = {
    "mocknet": ("docker-compose.yml", "mocknet", 5),
    "chainnet": ("docker-compose.xmr-chainnet.yml", "chainnet", 3),
    "stagenet": ("docker-compose.xmr-stagenet.yml", "mocknet", 3),
}


def render(profile, token=TEST_TOKEN, *, xmr_disabled="false"):
    env = os.environ.copy()
    # Validate XMR readiness even when the caller disables XMR for a cluster run.
    # Only this rendering subprocess receives the controlled setting.
    env["BIFROST_CHAINS_XMR_DISABLED"] = xmr_disabled
    for key in ("XMR_FROST_SIGNER_IMAGE", "XMR_FROST_AUTH_BEARER"):
        env.pop(key, None)
    if token is not None:
        env["XMR_FROST_AUTH_BEARER"] = token
    filename = PROFILES[profile][0]
    return subprocess.run(
        [
            "docker",
            "compose",
            "-p",
            "xmr-readiness-contract",
            "-f",
            str(ROOT / "build/docker" / filename),
            "--profile",
            "*",
            "config",
            "--format",
            "json",
        ],
        env=env,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


def seed_identity(service, model):
    env = service.get("environment", {})
    if env.get("SIGNER_SEED_PHRASE"):
        return env["SIGNER_SEED_PHRASE"].strip()
    seed_path = env.get("SIGNER_SEED_PHRASE_FILE")
    for mounted in service.get("configs", []):
        if mounted.get("target") == seed_path:
            seed_file = Path(model["configs"][mounted["source"]]["file"]).resolve()
            if not seed_file.is_relative_to(ROOT):
                raise AssertionError(
                    "readiness tests require a repository-owned test seed file"
                )
            return seed_file.read_text().strip()
    return None


class XMRReadinessConfigTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.models = {}
        for profile in PROFILES:
            result = render(profile)
            if result.returncode:
                raise AssertionError(
                    f"{profile} Compose configuration failed: {result.stderr}"
                )
            cls.models[profile] = json.loads(result.stdout)

    def test_render_controls_xmr_without_changing_the_caller_environment(self):
        setting = "BIFROST_CHAINS_XMR_DISABLED"
        with patch.dict(os.environ, {setting: "true"}):
            result = render("mocknet")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(os.environ[setting], "true")
            bifrosts = {
                name: service
                for name, service in json.loads(result.stdout)["services"].items()
                if name.startswith("bifrost")
            }
            self.assertEqual(len(bifrosts), PROFILES["mocknet"][2])
            for name, service in bifrosts.items():
                with self.subTest(service=name):
                    self.assertEqual(service["environment"][setting], "false")

    def test_mocknet_still_honors_explicit_xmr_disable(self):
        result = render("mocknet", xmr_disabled="true")
        self.assertEqual(result.returncode, 0, result.stderr)
        bifrosts = {
            name: service
            for name, service in json.loads(result.stdout)["services"].items()
            if name.startswith("bifrost")
        }
        self.assertEqual(len(bifrosts), PROFILES["mocknet"][2])
        for name, service in bifrosts.items():
            with self.subTest(service=name):
                self.assertEqual(
                    service["environment"]["BIFROST_CHAINS_XMR_DISABLED"], "true"
                )

    def test_mocknet_keysign_timeout_and_jail_are_consistent_across_nodes(self):
        services = self.models["mocknet"]["services"]
        for suffix in ("", "-cat", "-fox", "-pig", "-frog"):
            with self.subTest(node=suffix or "primary"):
                thornode_env = services[f"thornode{suffix}"]["environment"]
                bifrost_env = services[f"bifrost{suffix}"]["environment"]
                self.assertEqual(
                    bifrost_env.get("BIFROST_SIGNER_KEYSIGN_TIMEOUT"), "45s"
                )
                # At mocknet's one-second block time, jail must outlast signing.
                # Bifrost checks its local constant; THORNode enforces jail.
                self.assertEqual(thornode_env.get("JAIL_TIME_KEYSIGN"), "60")
                self.assertEqual(bifrost_env.get("JAIL_TIME_KEYSIGN"), "60")

    def test_mocknet_xmr_scanner_uses_simulation_backoff(self):
        for name, service in self.models["mocknet"]["services"].items():
            if not name.startswith("bifrost"):
                continue
            with self.subTest(service=name):
                env = service["environment"]
                self.assertEqual(
                    env.get(
                        "BIFROST_CHAINS_XMR_BLOCK_SCANNER_BLOCK_HEIGHT_DISCOVER_BACK_OFF"
                    ),
                    env["BLOCK_SCANNER_BACKOFF"],
                )

    def test_each_bifrost_can_authenticate_to_its_matching_signer(self):
        for profile, model in self.models.items():
            services = model["services"]
            _, network, count = PROFILES[profile]
            bifrosts = {n: s for n, s in services.items() if n.startswith("bifrost")}
            self.assertEqual(len(bifrosts), count, profile)
            signers = set()
            for name, bifrost in bifrosts.items():
                with self.subTest(profile=profile, service=name):
                    env = bifrost["environment"]
                    self.assertEqual(env.get("BIFROST_CHAINS_XMR_DISABLED"), "false")
                    endpoint = urlsplit(env["BIFROST_CHAINS_XMR_SIGNER_ENDPOINT"])
                    self.assertEqual(endpoint.scheme, "http")
                    self.assertTrue(endpoint.hostname in services, name)
                    signers.add(endpoint.hostname)
                    signer_env = services[endpoint.hostname]["environment"]
                    self.assertEqual(
                        env.get("BIFROST_CHAINS_XMR_SIGNER_EXPECTED_NETWORK"), network
                    )
                    self.assertEqual(signer_env.get("XMR_FROST_CHAIN_NETWORK"), network)
                    self.assertEqual(
                        env.get("BIFROST_CHAINS_XMR_SIGNER_ALLOW_INSECURE_HTTP"), "true"
                    )
                    bearer = env.get("BIFROST_CHAINS_XMR_SIGNER_AUTHORIZATION_BEARER")
                    self.assertTrue(
                        bearer, "Bifrost requires explicit signer authentication"
                    )
                    self.assertTrue(
                        bearer == signer_env.get("XMR_FROST_AUTH_BEARER"),
                        "Bifrost and signer authentication must match",
                    )
                    self.assertNotIn("BIFROST_CHAINS_XMR_AUTHORIZATION_BEARER", env)
            self.assertEqual(len(signers), count, "each validator needs its own signer")

    def test_validator_and_signer_seed_assignments_match(self):
        for profile, model in self.models.items():
            identities = set()
            for name, service in model["services"].items():
                if not name.startswith("bifrost"):
                    continue
                with self.subTest(profile=profile, service=name):
                    endpoint = urlsplit(
                        service["environment"]["BIFROST_CHAINS_XMR_SIGNER_ENDPOINT"]
                    )
                    identity = seed_identity(service, model)
                    self.assertTrue(
                        identity, "Bifrost seed must be explicitly assigned"
                    )
                    self.assertTrue(
                        identity
                        == seed_identity(model["services"][endpoint.hostname], model),
                        "validator and signer seed assignments must match",
                    )
                    self.assertNotIn(
                        identity,
                        identities,
                        "validators must have distinct seed assignments",
                    )
                    identities.add(identity)

    def test_non_mocknet_signers_use_mounted_seed_files(self):
        for profile, model in self.models.items():
            for name, service in model["services"].items():
                if not name.startswith("xmr-frost-signer"):
                    continue
                env = service["environment"]
                if env["XMR_FROST_CHAIN_NETWORK"] == "mocknet":
                    continue
                with self.subTest(profile=profile, service=name):
                    self.assertNotIn("SIGNER_SEED_PHRASE", env)
                    self.assertTrue(env.get("SIGNER_SEED_PHRASE_FILE"))
                    self.assertTrue(seed_identity(service, model))

    def test_signers_share_an_immutable_image_and_supported_rate_limits(self):
        images = set()
        for profile, model in self.models.items():
            for name, service in model["services"].items():
                if not name.startswith("xmr-frost-signer"):
                    continue
                with self.subTest(profile=profile, service=name):
                    self.assertRegex(service["image"], r"@sha256:[0-9a-f]{64}$")
                    images.add(service["image"])
                    env = service["environment"]
                    for setting in (
                        "XMR_FROST_MAX_REQUESTS_PER_SECOND",
                        "XMR_FROST_MAX_REQUESTS_PER_IP_SECOND",
                    ):
                        self.assertGreater(int(env.get(setting, "0")), 0, setting)
                    self.assertFalse(
                        any(k.startswith("XMR_FROST_RATE_LIMIT_") for k in env),
                        "legacy rate-limit settings do not configure this signer image",
                    )
        self.assertEqual(
            len(images), 1, "all profiles must use the same reviewed signer image"
        )

    def test_private_profiles_require_explicit_authentication(self):
        for profile in ("chainnet", "stagenet"):
            with self.subTest(profile=profile):
                result = render(profile, token=None)
                self.assertNotEqual(result.returncode, 0)
                self.assertTrue("XMR_FROST_AUTH_BEARER" in result.stderr)


if __name__ == "__main__":
    unittest.main()
