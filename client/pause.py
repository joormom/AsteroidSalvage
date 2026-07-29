"""In-game pause overlay.

ESC used to quit outright, which is a brutal thing to do to someone mid-match who just
wanted to check the controls. It now opens this: resume, settings, leave to the main
menu, or quit for real.

Settings reuses the same values dict the front-end settings screen writes, so a change
made here applies immediately rather than on next launch.
"""

from __future__ import annotations

from direct.gui.DirectGui import DirectButton, DirectFrame, DirectLabel

TITLE_COLOR = (1.0, 0.85, 0.35, 1.0)
BODY_COLOR = (0.84, 0.88, 0.95, 1.0)
MUTED = (0.55, 0.58, 0.66, 1.0)
BLUE = (0.20, 0.32, 0.55, 1.0)
GREY = (0.18, 0.20, 0.26, 1.0)
RED = (0.48, 0.18, 0.18, 1.0)


class PauseMenu:
    def __init__(self, base, settings, on_resume, on_leave, on_exit):
        self.base = base
        self.settings = settings
        self.on_resume = on_resume
        self.on_leave = on_leave
        self.on_exit = on_exit
        self.visible = False

        self.root = DirectFrame(
            frameColor=(0.02, 0.03, 0.06, 0.90),
            frameSize=(-2.2, 2.2, -1.1, 1.1),
            parent=base.aspect2d,
        )

        self.main = DirectFrame(frameColor=(0, 0, 0, 0), parent=self.root)
        DirectLabel(
            text="PAUSED", scale=0.11, pos=(0, 0, 0.52), text_fg=TITLE_COLOR,
            frameColor=(0, 0, 0, 0), parent=self.main,
        )
        self._button(self.main, "RESUME", 0.24, self._resume, BLUE)
        self._button(self.main, "SETTINGS", 0.04, self._open_settings, GREY)
        self._button(self.main, "LEAVE GAME", -0.20, self._leave, GREY)
        self._button(self.main, "EXIT GAME", -0.44, self.on_exit, RED)
        DirectLabel(
            text="ESC to resume", scale=0.042, pos=(0, 0, -0.64), text_fg=MUTED,
            frameColor=(0, 0, 0, 0), parent=self.main,
        )

        self.settings_panel = self._build_settings()
        self.settings_panel.hide()

        self.root.hide()

    def _button(self, parent, text, y, command, color):
        return DirectButton(
            text=text, scale=0.07, pos=(0, 0, y), frameColor=color,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.42, 0.13),
            command=command, parent=parent,
        )

    def _build_settings(self):
        from menu import Cycler  # local import: avoids a circular import at module load

        f = DirectFrame(frameColor=(0, 0, 0, 0), parent=self.root)
        DirectLabel(
            text="SETTINGS", scale=0.095, pos=(0, 0, 0.52), text_fg=TITLE_COLOR,
            frameColor=(0, 0, 0, 0), parent=f,
        )

        sens_values = [0.008, 0.012, 0.018, 0.026, 0.036, 0.05]
        try:
            idx = sens_values.index(self.settings.get("sensitivity", 0.018))
        except ValueError:
            idx = 2

        Cycler(
            f, "Mouse sensitivity", sens_values, idx, 0.26,
            lambda v: f"{sens_values.index(v) + 1} of {len(sens_values)}",
            on_change=self._set_sensitivity,
        )
        Cycler(
            f, "Invert mouse Y", [0, 1], 1 if self.settings.get("invert_y") else 0, 0.12,
            lambda v: "On" if v else "Off",
            on_change=self._set_invert,
        )

        from menu import _nearest, _pct

        vols = [0.0, 0.2, 0.4, 0.6, 0.8, 1.0]
        Cycler(
            f, "Sound volume", vols,
            _nearest(vols, self.settings.get("sfx_volume", 0.7)), -0.02, _pct,
            on_change=lambda v: self._set_volume("sfx_volume", v),
        )
        Cycler(
            f, "Music volume", vols,
            _nearest(vols, self.settings.get("music_volume", 0.5)), -0.16, _pct,
            on_change=lambda v: self._set_volume("music_volume", v),
        )

        DirectLabel(
            text=(
                "W/S thrust    A/D strafe    MOUSE aim\n"
                "LEFT-CLICK  laser        HOLD RIGHT-CLICK  tractor beam\n"
                "HOLD SHIFT boost    SPACE brake    TAB release mouse"
            ),
            scale=0.046, pos=(0, 0, -0.34), text_fg=MUTED,
            frameColor=(0, 0, 0, 0), parent=f,
        )
        self._button(f, "BACK", -0.58, self._close_settings, GREY)
        return f

    def _set_volume(self, key: str, value: float) -> None:
        self.settings[key] = value
        audio = getattr(self.base, "audio", None)
        if audio is not None:
            audio.apply_volumes()

    # Applied live rather than on close, so the player can feel the change while the
    # menu is still open.
    def _set_sensitivity(self, v):
        self.settings["sensitivity"] = v
        self.base.controls.sensitivity = v

    def _set_invert(self, v):
        self.settings["invert_y"] = bool(v)
        self.base.controls.invert_y = bool(v)

    # --- visibility --------------------------------------------------------

    def toggle(self) -> None:
        self.hide() if self.visible else self.show()

    def show(self) -> None:
        self.visible = True
        self.main.show()
        self.settings_panel.hide()
        self.root.show()
        # The cursor is the interface while paused.
        self.base.controls.release_mouse()

    def hide(self) -> None:
        self.visible = False
        self.root.hide()

    def _resume(self) -> None:
        self.hide()
        self.on_resume()

    def _leave(self) -> None:
        self.hide()
        self.on_leave()

    def _open_settings(self) -> None:
        self.main.hide()
        self.settings_panel.show()

    def _close_settings(self) -> None:
        self.settings_panel.hide()
        self.main.show()

    def destroy(self) -> None:
        self.root.destroy()
