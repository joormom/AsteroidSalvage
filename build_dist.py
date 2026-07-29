"""Build the shareable Windows package.

    python build_dist.py

Produces `dist/AsteroidSalvage-win64/` containing everything a friend needs — the game,
the server, and instructions — plus a zip of the same next to it. Nothing needs to be
installed on their machine.

Steps, in order:
  1. Build the Go server as a standalone exe (only depends on DLLs Windows ships with).
  2. Stage shared/asteroid_protocol.py next to the client so the freezer can find it.
  3. Run Panda3D's build_apps to freeze the client.
  4. Assemble the folder, write the README, and zip it.
"""

from __future__ import annotations

import os
import shutil
import subprocess
import sys
import zipfile

ROOT = os.path.dirname(os.path.abspath(__file__))
DIST = os.path.join(ROOT, "dist")
BUILD = os.path.join(ROOT, "build")
OUT_NAME = "AsteroidSalvage-win64"
OUT_DIR = os.path.join(DIST, OUT_NAME)

# Where the toolchains live on this machine. Overridable via the environment so the
# build is not welded to one PC.
GO_BIN = os.environ.get("GO_BIN", r"C:\Program Files\Go\bin")
MINGW_BIN = os.environ.get(
    "MINGW_BIN",
    os.path.expandvars(
        r"%LOCALAPPDATA%\Microsoft\WinGet\Packages"
        r"\BrechtSanders.WinLibs.POSIX.UCRT_Microsoft.Winget.Source_8wekyb3d8bbwe"
        r"\mingw64\bin"
    ),
)

README = """ASTEROID SALVAGE
================

Fly out, tractor asteroids back to your mothership, bank them before the round ends.
Best of 5 rounds. Buy ship upgrades between rounds.

Nothing to install. Just run the game.


PLAYING
-------

  1. Run AsteroidSalvage.exe
  2. One person clicks HOST A GAME. The screen shows their address, e.g. 192.168.1.42
  3. Everyone else types that address and clicks JOIN

Playing alone? Just click HOST A GAME.

Everyone must be on the same network (same Wi-Fi or router).


CONTROLS
--------

  W / S              thrust forward / back
  A / D              strafe left / right
  MOUSE              aim
  LEFT-CLICK         fire the laser
  HOLD RIGHT-CLICK   tractor beam - grab and pull
  SHIFT              boost (hold it - the yellow bar is your tank)
  SPACE              brake
  TAB                release the mouse
  ESC                pause menu

  1-4                buy upgrades (between rounds only)


HOW IT WORKS
------------

Point at an asteroid within about 15 m and hold RIGHT-CLICK. The beam locks on and
reels it in. Haul it back to YOUR OWN mothership - the one in your team's colours -
and get close; it takes the cargo automatically. Another team's hangar pays you
nothing.

Hauling is heavy. A big rock fights your steering, and slamming into things damages
the cargo, which cuts what it pays. Slow is usually faster.

Asteroid colours tell you what they are worth:

  grey     rubble    nearly worthless
  rust     iron      solid, reliable
  cyan     crystal   light and very valuable - worth the detour
  yellow   gold      heavy, pays well
  dark red MASSIVE   huge payout, but TOO HEAVY FOR ONE SHIP
  violet   COLOSSAL  cannot be towed at all - shoot it apart
  near-black TITAN   bigger than your mothership. Later rounds only, and a stock
                     laser will not cut it.
  white    CORE      the seam inside a big rock. Worth more than anything else.

A massive asteroid will not move at all for a single beam. Get a teammate to grab it
too, or buy the Tractor Amplifier upgrade. Whoever helps shares the payout.

COLOSSAL and TITAN rocks cannot be towed by anything. Shoot them apart: the fragments
are haulable, and buried inside each one is a CORE seam worth more than a whole run of
ordinary rocks. A titan takes a properly upgraded gun, or a whole crew firing at once.


COMBAT
------

LEFT-CLICK fires a laser. It carries all the way across the map and only stops when it
leaves the play area, so a lined-up shot connects from a long way out.

The blue bar at the bottom is your charge: six shots, refilling over five seconds.
Empty it and you are defenceless until it comes back. The green bar above it is your
hull - four hits and you are destroyed, out for four seconds, and your cargo is
dropped where you died.

You cannot hit your own team. Buy Laser Focuser, Capacitor Bank and Fast Recharger
between rounds to hit harder, longer, and more often.


BOOST
-----

The yellow bar with the lightning bolt is your boost tank: four seconds of double
thrust, refilling over seven. Hold SHIFT and it keeps burning until you let go or the
tank runs dry - and if you run it dry you have to release the key before it works
again. The map is large; spend it on the long legs, not on shunting between rocks.


TROUBLESHOOTING
---------------

"could not connect"       The host has not clicked HOST A GAME yet, or the address is
                          wrong, or you are on a different network.

Windows Firewall prompt   The host must allow it, or nobody can join.

Nothing happens on HOST   server.exe must sit in the same folder as the game.
"""


def run(cmd: list[str], cwd: str, env: dict | None = None) -> None:
    print(f"  $ {' '.join(cmd)}")
    result = subprocess.run(cmd, cwd=cwd, env=env)
    if result.returncode != 0:
        sys.exit(f"FAILED: {' '.join(cmd)}")


def build_server() -> str:
    print("[1/4] building the server")
    env = dict(os.environ)
    env["PATH"] = GO_BIN + os.pathsep + MINGW_BIN + os.pathsep + env.get("PATH", "")
    env["CGO_ENABLED"] = "1"

    os.makedirs(os.path.join(ROOT, "bin"), exist_ok=True)
    exe = os.path.join(ROOT, "bin", "server.exe")

    # Full path to go.exe: on Windows CreateProcess resolves the program name against
    # the *parent* process's PATH, not the environment passed to subprocess, so putting
    # GO_BIN in env["PATH"] is not enough to find it. (env["PATH"] still matters — it is
    # how the Go toolchain itself finds gcc for cgo.)
    go = os.path.join(GO_BIN, "go.exe")
    if not os.path.isfile(go):
        go = "go"  # fall back to whatever is already on PATH
    run([go, "build", "-o", exe, "."], cwd=os.path.join(ROOT, "server"), env=env)

    size = os.path.getsize(exe) / (1024 * 1024)
    print(f"      server.exe ({size:.1f} MB)")
    return exe


def stage_protocol() -> str:
    """Copy the protocol module next to the client for the freezer.

    The client finds it at runtime via a sys.path insert, which the freezer's static
    import analysis cannot follow. shared/ stays the single source of truth; this copy
    is generated and gitignored.
    """
    print("[2/4] staging the shared protocol module")
    src = os.path.join(ROOT, "shared", "asteroid_protocol.py")
    dst = os.path.join(ROOT, "client", "asteroid_protocol.py")
    shutil.copy2(src, dst)
    return dst


def build_client() -> str:
    print("[3/4] freezing the client (downloads Panda3D runtime on first run)")
    run([sys.executable, "setup.py", "build_apps"], cwd=ROOT)

    built = os.path.join(BUILD, "win_amd64")
    if not os.path.isdir(built):
        sys.exit(f"expected build output at {built}")
    return built


def assemble(server_exe: str, client_dir: str) -> None:
    print("[4/4] assembling the package")

    if os.path.isdir(OUT_DIR):
        try:
            shutil.rmtree(OUT_DIR)
        except PermissionError as exc:
            # Almost always a server or game from a previous test run still holding its
            # own executable open. Say so plainly rather than dumping a rmtree traceback.
            sys.exit(
                f"\nCannot clear {OUT_DIR}\n  {exc}\n\n"
                "Something in that folder is still running. Close the game, then:\n"
                '  taskkill /f /im server.exe\n'
                '  taskkill /f /im AsteroidSalvage.exe\n'
            )
    os.makedirs(OUT_DIR, exist_ok=True)

    # Frozen client, then the server beside it — the HOST button looks for it there.
    for entry in os.listdir(client_dir):
        src = os.path.join(client_dir, entry)
        dst = os.path.join(OUT_DIR, entry)
        if os.path.isdir(src):
            shutil.copytree(src, dst)
        else:
            shutil.copy2(src, dst)

    shutil.copy2(server_exe, os.path.join(OUT_DIR, "server.exe"))

    cfg_dir = os.path.join(OUT_DIR, "config")
    os.makedirs(cfg_dir, exist_ok=True)
    shutil.copy2(os.path.join(ROOT, "config", "feel.toml"), os.path.join(cfg_dir, "feel.toml"))

    with open(os.path.join(OUT_DIR, "READ ME FIRST.txt"), "w", encoding="utf-8") as f:
        f.write(README)

    # Zip it for sharing.
    zip_path = os.path.join(DIST, f"{OUT_NAME}.zip")
    if os.path.exists(zip_path):
        os.remove(zip_path)

    print(f"      zipping -> {zip_path}")
    with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED, compresslevel=6) as z:
        for folder, _dirs, files in os.walk(OUT_DIR):
            for name in files:
                full = os.path.join(folder, name)
                z.write(full, os.path.join(OUT_NAME, os.path.relpath(full, OUT_DIR)))

    folder_mb = sum(
        os.path.getsize(os.path.join(dp, f))
        for dp, _dn, fn in os.walk(OUT_DIR)
        for f in fn
    ) / (1024 * 1024)
    zip_mb = os.path.getsize(zip_path) / (1024 * 1024)

    print()
    print("DONE")
    print(f"  folder : {OUT_DIR}  ({folder_mb:.0f} MB)")
    print(f"  zip    : {zip_path}  ({zip_mb:.0f} MB)   <- share this")


def main() -> None:
    server = build_server()
    stage_protocol()
    client = build_client()
    assemble(server, client)


if __name__ == "__main__":
    main()
