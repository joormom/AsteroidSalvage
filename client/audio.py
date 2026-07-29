"""Audio playback: loads the generated sounds and plays them on game events.

Everything degrades to silence. If the cache cannot be written, or the audio device is
missing, or a file fails to load, the game plays on without sound — losing a match to a
missing audio driver would be absurd.
"""

from __future__ import annotations

import os
import sys

from panda3d.core import Filename

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

import sfx  # noqa: E402


class Audio:
    def __init__(self, base, settings):
        self.base = base
        self.settings = settings
        self.enabled = False

        self.boost = None
        self.deposit = None
        self.beam = None
        self.ambience = None
        self.menu_music = None
        self.laser = None
        self.explosion = None
        self.boost_loop = None

        self._beam_on = False
        self._boost_on = False

        directory = sfx.ensure_sounds()
        if not directory:
            return

        def load(name: str):
            # Panda3D wants Unix-style paths; handing it a Windows path fails to open
            # the file and it warns "expected Unix-style path".
            path = Filename.fromOsSpecific(os.path.join(directory, name))
            return base.loader.loadSfx(path)

        try:
            self.boost = load("boost.wav")
            self.deposit = load("deposit.wav")
            self.beam = load("beam.wav")
            self.ambience = load("ambience.wav")
            self.menu_music = load("menu.wav")
            self.laser = load("laser.wav")
            self.explosion = load("explosion.wav")
            self.boost_loop = load("boostloop.wav")
        except Exception:  # noqa: BLE001 - silence is an acceptable outcome
            return

        # A failed load returns a valid but empty AudioSound, not None — checking for
        # None alone reported working audio while playing nothing at all. Length is the
        # honest test.
        sounds = (self.boost, self.deposit, self.beam, self.ambience, self.menu_music,
                  self.laser, self.explosion, self.boost_loop)
        if not all(s is not None and s.length() > 0.01 for s in sounds):
            return

        for loop in (self.beam, self.ambience, self.menu_music, self.boost_loop):
            loop.setLoop(True)

        self.enabled = True
        self.apply_volumes()

    # --- volume ------------------------------------------------------------

    def apply_volumes(self) -> None:
        if not self.enabled:
            return
        sfx_vol = float(self.settings.get("sfx_volume", 0.7))
        music_vol = float(self.settings.get("music_volume", 0.5))

        self.boost.setVolume(sfx_vol * 0.9)
        self.deposit.setVolume(sfx_vol)
        # The beam and ambience sit under everything else and would be fatiguing at
        # full level — they are long loops, not one-shots.
        self.beam.setVolume(sfx_vol * 0.55)
        self.ambience.setVolume(music_vol * 0.5)
        self.menu_music.setVolume(music_vol)
        # The laser fires several times a second, so it sits well under the one-shots
        # that mark an actual outcome.
        self.laser.setVolume(sfx_vol * 0.45)
        self.explosion.setVolume(sfx_vol)
        # A held-down roar sits under the one-shots; at full level it would drown out
        # the deposit chime and the laser, which are the sounds that mean something.
        self.boost_loop.setVolume(sfx_vol * 0.5)

    # --- one-shots ---------------------------------------------------------

    def play_boost(self) -> None:
        if self.enabled:
            self.boost.play()

    def play_deposit(self) -> None:
        if self.enabled:
            self.deposit.play()

    def play_laser(self) -> None:
        if self.enabled:
            # Restart rather than overlap: rapid fire layered on itself turns into a
            # single sustained tone and loses the per-shot rhythm entirely.
            self.laser.stop()
            self.laser.play()

    def play_explosion(self) -> None:
        if self.enabled:
            self.explosion.play()

    def set_boost(self, on: bool) -> None:
        """Follow the engines: ignition whoosh on, sustained roar while burning.

        Driven by whether the ship is *actually* boosting, not by the key — the tank can
        be empty, and a roar with nothing happening is worse than silence. The roar stops
        the moment the player lets go or runs dry.
        """
        if not self.enabled or on == self._boost_on:
            return
        self._boost_on = on
        if on:
            self.play_boost()  # the ignition whoosh, once
            self.boost_loop.play()
        else:
            self.boost_loop.stop()

    # --- loops -------------------------------------------------------------

    def set_beam(self, on: bool) -> None:
        if not self.enabled or on == self._beam_on:
            return
        self._beam_on = on
        self.beam.play() if on else self.beam.stop()

    def set_in_game(self, in_game: bool) -> None:
        """Swap between menu music and space ambience."""
        if not self.enabled:
            return
        if in_game:
            self.menu_music.stop()
            if self.ambience.status() != self.ambience.PLAYING:
                self.ambience.play()
        else:
            self.ambience.stop()
            self.set_beam(False)
            self.set_boost(False)
            if self.menu_music.status() != self.menu_music.PLAYING:
                self.menu_music.play()

    def stop_all(self) -> None:
        if not self.enabled:
            return
        for s in (self.beam, self.ambience, self.menu_music, self.boost_loop):
            s.stop()
        self._beam_on = False
        self._boost_on = False
