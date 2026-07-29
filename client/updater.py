"""Self-update from GitHub releases.

The problem this solves is social, not technical: the game is handed to friends as a zip,
and without this every fix means re-sending a 34 MB file and asking six people to unzip it
again. With it they keep one shortcut forever.

How it works, and why each part is the way it is:

  - **The check is cheap and non-blocking.** One HTTPS request to the releases API on a
    background thread. If GitHub is down, the machine is offline, or the repo does not
    exist yet, the game must start anyway — an updater that can stop you playing is worse
    than no updater.
  - **A running .exe cannot overwrite itself on Windows.** So the new build is staged in a
    temp folder and a small batch script does the swap after the game exits. That script
    is the only part that runs while nothing is holding the files open.
  - **Nothing is swapped without a complete download.** The zip is fetched to a temporary
    file and extracted to a staging folder *before* the game is told an update is ready,
    so a connection dropped halfway leaves the installed copy untouched.
  - **Source runs never update themselves.** Updating a git checkout out from under a
    developer would be hostile, so this is inert unless the game is running frozen.

Versions are compared as tuples of integers parsed from the tag, so `v1.10.0` correctly
beats `v1.9.0` — a string compare would not.
"""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import urllib.error
import urllib.request
import zipfile

# Where updates come from. Overridable so a fork or a test can point elsewhere without
# editing code.
REPO = os.environ.get("ASTEROID_UPDATE_REPO", "joormom/AsteroidSalvage")
RELEASES_URL = f"https://api.github.com/repos/{REPO}/releases/latest"

# Short: this runs during startup, and a slow network must not become a slow launch.
TIMEOUT_SECONDS = 6.0

# GitHub rejects API requests without one.
USER_AGENT = "AsteroidSalvage-Updater"


def _version_tuple(text: str) -> tuple[int, ...]:
    """Parse 'v1.10.0' into (1, 10, 0). Unparseable versions sort lowest."""
    nums = re.findall(r"\d+", text or "")
    return tuple(int(n) for n in nums) if nums else (0,)


def current_version() -> str:
    """The version baked in at build time, or a source-run marker.

    build_dist.py writes client/buildinfo.py. Its absence is how a source checkout is
    detected, which is also why this import is deliberately not at module scope.
    """
    try:
        import buildinfo  # noqa: PLC0415 - absence is meaningful, see above

        return getattr(buildinfo, "VERSION", "0.0.0")
    except ImportError:
        return "source"


def is_frozen() -> bool:
    """True when running from a packaged build rather than a source checkout.

    Only `sys.frozen`, which Panda3D's deploy sets. An earlier version also guessed from
    the layout around sys.argv[0] and got it backwards under `python -c`, where argv[0] is
    "-c" and the probe found no client directory — so a source checkout reported itself as
    packaged. That is the one direction this must never be wrong in: it would let the
    updater overwrite somebody's working tree.
    """
    return bool(getattr(sys, "frozen", False))


def install_root() -> str:
    """The folder holding the packaged game — what gets replaced."""
    return os.path.dirname(os.path.abspath(sys.argv[0]))


def _fetch_latest() -> dict | None:
    req = urllib.request.Request(RELEASES_URL, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_SECONDS) as resp:
            return json.loads(resp.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, ValueError, OSError):
        # Offline, rate-limited, no releases published yet, or a malformed reply. None of
        # these are worth troubling the player with — they just mean "no update today".
        return None


def _zip_asset(release: dict) -> str | None:
    for asset in release.get("assets", ()):
        name = asset.get("name", "")
        if name.endswith(".zip"):
            return asset.get("browser_download_url")
    return None


def check(on_ready) -> None:
    """Look for a newer release in the background.

    `on_ready(version, staged_dir)` is called only once a complete, extracted copy is
    sitting on disk ready to install. It runs on the worker thread, so anything touching
    Panda3D must hop back to the main thread itself.
    """
    if not is_frozen():
        return  # never update a source checkout

    threading.Thread(target=_worker, args=(on_ready,), daemon=True).start()


def _worker(on_ready) -> None:
    try:
        release = _fetch_latest()
        if not release:
            return

        latest = release.get("tag_name", "")
        if _version_tuple(latest) <= _version_tuple(current_version()):
            return

        url = _zip_asset(release)
        if not url:
            return

        staged = _download_and_extract(url)
        if staged:
            on_ready(latest, staged)
    except Exception:  # noqa: BLE001
        # A background updater must never take the game down with it.
        return


def _download_and_extract(url: str) -> str | None:
    """Fetch the zip and unpack it into a staging folder. Returns the folder, or None."""
    tmp_dir = tempfile.mkdtemp(prefix="asteroidsalvage-update-")
    archive = os.path.join(tmp_dir, "update.zip")

    req = urllib.request.Request(url, headers={"User-Agent": USER_AGENT})
    try:
        with urllib.request.urlopen(req, timeout=60) as resp, open(archive, "wb") as out:
            shutil.copyfileobj(resp, out)

        staged = os.path.join(tmp_dir, "unpacked")
        with zipfile.ZipFile(archive) as zf:
            zf.extractall(staged)
    except Exception:  # noqa: BLE001
        shutil.rmtree(tmp_dir, ignore_errors=True)
        return None

    # Releases are zipped with a single top-level folder; install its contents, not the
    # folder itself, or every update nests one directory deeper than the last.
    entries = [os.path.join(staged, e) for e in os.listdir(staged)]
    if len(entries) == 1 and os.path.isdir(entries[0]):
        return entries[0]
    return staged


def apply_and_restart(staged: str) -> None:
    """Hand off to a script that swaps the files once this process has exited.

    The swap cannot happen here: Windows holds a lock on the running executable and on
    every DLL loaded beside it. So this writes a batch file that waits for the process to
    disappear, copies the new build over the old one, relaunches the game and deletes
    itself.
    """
    root = install_root()
    exe = os.path.abspath(sys.argv[0])
    script = os.path.join(tempfile.gettempdir(), "asteroidsalvage-update.bat")

    with open(script, "w", encoding="utf-8") as f:
        f.write(
            "@echo off\r\n"
            "rem Wait for the game to let go of its own files before touching them.\r\n"
            f':wait\r\n'
            f'tasklist /fi "PID eq {os.getpid()}" 2>nul | find "{os.getpid()}" >nul\r\n'
            "if not errorlevel 1 (\r\n"
            "  ping -n 2 127.0.0.1 >nul\r\n"
            "  goto wait\r\n"
            ")\r\n"
            f'xcopy /e /y /q "{staged}\\*" "{root}\\" >nul\r\n'
            f'start "" "{exe}"\r\n'
            f'del "%~f0"\r\n'
        )

    # DETACHED_PROCESS, so the helper outlives the game it is waiting for.
    subprocess.Popen(
        ["cmd", "/c", script],
        creationflags=0x00000008 | 0x08000000,  # DETACHED_PROCESS | CREATE_NO_WINDOW
        close_fds=True,
    )
