"""Validate deployment interpolation without building images or using a daemon."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class ComposeConfigTest(unittest.TestCase):
    def render(self, thumb_cache):
        # Isolate from local .env files and exported deployment credentials.
        env = {key: value for key, value in os.environ.items()
               if not key.startswith(("MUSIC_SERVER_", "MUSIC_ANALYZER_", "COMPOSE_"))}
        settings = [
            "MUSIC_SERVER_ADMIN_PASSWORD=compose-test-password",
            "MUSIC_SERVER_API_TOKEN=compose-test-api-token-12345678",
            "MUSIC_SERVER_MEDIA_TOKEN=compose-test-media-token-12345678",
            "MUSIC_SERVER_MUSIC_PATH=/tmp/compose-test-music",
        ]
        if thumb_cache is not None:
            settings.append(f"MUSIC_SERVER_THUMB_CACHE_MB={thumb_cache}")
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / ".env"
            env_file.write_text("\n".join(settings) + "\n", encoding="utf-8")
            result = subprocess.run(
                ["docker", "compose", "--project-directory", str(ROOT),
                 "--env-file", str(env_file), "-f", str(ROOT / "compose.yaml"),
                 "config", "--format", "json"],
                env=env, check=True, capture_output=True, text=True, timeout=30,
            )
        return json.loads(result.stdout)["services"]["music-server"]

    def test_thumbnail_cache_from_env_file(self):
        for configured, expected in [(None, "512"), ("", "512"),
                                     ("512", "512"), ("128", "128")]:
            with self.subTest(configured=configured):
                service = self.render(configured)
                self.assertEqual(service["environment"]["MUSIC_SERVER_THUMB_CACHE_MB"],
                                 expected)

    def test_existing_deployment_settings_are_preserved(self):
        service = self.render("128")
        self.assertEqual(service["environment"]["MUSIC_SERVER_TRANSCODE_CACHE_MB"], "4096")
        self.assertEqual(service["environment"]["MUSIC_SERVER_ADMIN_PASSWORD"],
                         "compose-test-password")
        mounts = {mount["target"]: mount for mount in service["volumes"]}
        self.assertTrue(mounts["/music"]["read_only"])
        self.assertEqual(mounts["/music"]["source"], "/tmp/compose-test-music")
        self.assertEqual(mounts["/data"]["source"], str(ROOT / "data"))
        self.assertFalse(mounts["/data"].get("read_only", False))


if __name__ == "__main__":
    unittest.main()
