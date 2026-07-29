"""Packaging config for the standalone Windows build.

Uses Panda3D's own `build_apps` packager, which freezes the Python client and bundles the
interpreter and engine libraries so the recipient needs nothing installed.

Do not run this directly — use `python build_dist.py`, which stages the shared protocol
module, runs this, and assembles the final folder with the server executable alongside.
"""

from setuptools import setup

setup(
    name="AsteroidSalvage",
    version="1.0.0",
    description="Haul asteroids home. Best of 5 rounds.",
    options={
        "build_apps": {
            # A GUI app so Windows does not open a console window behind the game.
            "gui_apps": {"AsteroidSalvage": "client/main.py"},
            "log_filename": "$USER_APPDATA/AsteroidSalvage/game.log",
            "log_append": False,
            "platforms": ["win_amd64"],
            # pandagl is the renderer. Audio plugins are included so adding sound later
            # does not mean rebuilding the packaging setup.
            "plugins": ["pandagl", "p3openal_audio"],
            "include_patterns": [
                "config/*.toml",
            ],
            # The freezer follows imports from the entry script, but the client reaches
            # the protocol module through a runtime sys.path insert, which static
            # analysis cannot see. build_dist.py stages a copy next to the client and
            # these force it (and websockets, imported lazily inside a thread) in.
            "include_modules": {
                "AsteroidSalvage": [
                    "asteroid_protocol",
                    "websockets",
                    "websockets.sync",
                    "websockets.sync.client",
                    # Reached only through a local import inside Menus, which the
                    # freezer's static analysis does not follow.
                    "shipeditor",
                    # Generated at build time and imported lazily inside updater.py,
                    # so nothing the freezer can see points at either of them.
                    "buildinfo",
                    "updater",
                ]
            },
            # Trim the download: the client renders and talks WebSocket, nothing else.
            "exclude_modules": {
                "AsteroidSalvage": [
                    "tkinter",
                    "unittest",
                    "pydoc",
                    "doctest",
                    "pdb",
                    "numpy.testing",
                ]
            },
        }
    },
)
