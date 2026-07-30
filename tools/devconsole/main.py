"""Asteroid Salvage dev tuning console.

    python tools/devconsole/main.py
    python tools/devconsole/main.py --url ws://localhost:8090/debug

A standalone ImGui window that connects to the server's debug channel. It is NOT part of
the game client and shares no GL context with Panda3D, which is exactly what makes it
cheap: imgui-bundle brings its own window and render loop, so there is no custom backend
to write.

Why it exists: grab feel cannot be unit-tested. Tuning it by editing a constant,
rebuilding cgo, restarting the server and re-flying is a ~60 second loop. With sliders it
is instant, and that difference is what decides whether the game feels good.

Dragging a slider sends DebugSet; the server applies it on the next tick.
"""

from __future__ import annotations

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "shared"))

import numpy as np  # noqa: E402

import asteroid_protocol as proto  # noqa: E402
from debugnet import DebugClient  # noqa: E402

from imgui_bundle import hello_imgui, imgui, immapp  # noqa: E402

# Written by the server on shutdown; shown here so the two are not confused.
FEEL_PATH = os.path.join(os.path.dirname(__file__), "..", "..", "config", "feel.toml")

# The parameters that decide whether hauling feels right. Grouped first and expanded by
# default because they are the reason to open this window at all.
GRAB_PARAMS = {
    "grab.spring",
    "grab.damping",
    "grab.max_force",
    "grab.hold_distance",
    "grab.reaction_scale",
    "grab.range",
}
SHIP_PARAMS = {
    "ship.thrust",
    "ship.torque",
    "ship.linear_damping",
    "ship.angular_damping",
    "ship.rate_ki",
    "ship.rate_kd",
    "ship.input_deadzone",
    "ship.input_exponent",
}


class Console:
    def __init__(self, url: str):
        self.net = DebugClient(url)
        self.net.start()
        self.url = url

        # The server has no "read current tuning" message, so the console starts from
        # the documented defaults and owns the values from then on. Anything it sends
        # is authoritative; anything it has not sent is whatever the server loaded.
        self.values: dict[str, float] = dict(proto.PARAM_DEFAULTS)
        self.dirty: set[str] = set()
        self.status = "starting"

        # Used by --frames to render a bounded number of frames and exit, so the real
        # draw path can be verified without a human closing a window.
        self.frame_limit: int | None = None
        self.frames = 0

    # --- UI ---------------------------------------------------------------

    def draw(self):
        self.frames += 1
        if self.frame_limit is not None and self.frames >= self.frame_limit:
            hello_imgui.get_runner_params().app_shall_exit = True

        imgui.begin("Asteroid Salvage — tuning")

        self._draw_connection()
        imgui.separator()

        imgui.separator_text("Grab — the feel of hauling")
        self._sliders(GRAB_PARAMS)
        imgui.text_wrapped(
            "reaction_scale is the important one: it scales the force the cargo pushes "
            "back on your ship. 1.0 is physically correct; the value that feels right "
            "may not be."
        )

        imgui.separator_text("Ship handling")
        self._sliders(SHIP_PARAMS)
        imgui.text_wrapped(
            "Rotation is a rate loop (ART_OF_FLIGHT): torque and angular_damping are the "
            "turn rate you ask for and the gain that holds it. rate_kd adds virtual "
            "rotational inertia — a heavier ship, less thrown by impacts. rate_ki does "
            "nothing until something applies a sustained torque, which nothing yet does."
        )

        imgui.separator_text("Salvage fragility")
        self._sliders(
            {p[1] for p in proto.PARAMS} - GRAB_PARAMS - SHIP_PARAMS
        )

        imgui.separator()
        self._draw_actions()
        imgui.separator()
        self._draw_stats()

        imgui.end()

    def _draw_connection(self):
        if self.net.connected:
            imgui.text_colored(imgui.ImVec4(0.4, 0.9, 0.5, 1.0), f"connected  {self.url}")
        else:
            imgui.text_colored(imgui.ImVec4(1.0, 0.5, 0.4, 1.0), "disconnected")
            if self.net.error:
                imgui.text_wrapped(self.net.error)
            imgui.text_disabled(
                f"retrying (attempt {self.net.attempts}) — is the server running with -dev?"
            )

    def _sliders(self, names: set[str]):
        for pid, name, lo, hi in proto.PARAMS:
            if name not in names:
                continue

            # Log scale for the wide ranges; a linear 0-20000 slider makes the useful
            # low end of max_force impossible to select.
            flags = (
                imgui.SliderFlags_.logarithmic.value if hi >= 1000 else 0
            )
            changed, value = imgui.slider_float(
                name, self.values[name], lo, hi, flags=flags
            )
            if changed:
                self.values[name] = value
                if self.net.set_param(pid, value):
                    self.dirty.add(name)
                    self.status = f"set {name} = {value:g}"
                else:
                    self.status = "not connected — change not sent"

    def _draw_actions(self):
        if imgui.button("Reset to defaults"):
            for pid, name, _lo, _hi in proto.PARAMS:
                value = proto.PARAM_DEFAULTS[name]
                self.values[name] = value
                self.net.set_param(pid, value)
            self.dirty.clear()
            self.status = "reset all parameters to defaults"

        imgui.same_line()
        if imgui.button("Push all"):
            # Useful after a server restart: re-apply everything on screen.
            sent = 0
            for pid, name, _lo, _hi in proto.PARAMS:
                if self.net.set_param(pid, self.values[name]):
                    sent += 1
            self.status = f"pushed {sent} parameters"

        imgui.text_disabled(
            "The server writes config/feel.toml on shutdown (Ctrl-C), so a tuned "
            "session is kept."
        )
        imgui.text_disabled(self.status)

    def _draw_stats(self):
        imgui.separator_text("Server")
        stats = self.net.latest
        if stats is None:
            imgui.text_disabled("no stats yet")
            return

        imgui.text(f"bodies {stats.bodies}    sessions {stats.sessions}")

        budget = 1000.0 / 30.0
        pct = stats.tick_ms / budget * 100.0
        color = (
            imgui.ImVec4(0.4, 0.9, 0.5, 1.0)
            if pct < 50
            else imgui.ImVec4(1.0, 0.75, 0.3, 1.0)
            if pct < 85
            else imgui.ImVec4(1.0, 0.4, 0.35, 1.0)
        )
        imgui.text_colored(
            color, f"tick {stats.tick_ms:.2f} ms  ({pct:.0f}% of the {budget:.1f} ms budget)"
        )

        history = self.net.tick_ms_history
        if len(history) > 2:
            # plot_lines binds to a float32 ndarray; a Python list is rejected.
            values = np.fromiter(history, dtype=np.float32, count=len(history))
            imgui.plot_lines(
                "##tick_ms",
                values,
                scale_min=0.0,
                scale_max=max(budget, float(values.max())),
                graph_size=imgui.ImVec2(0, 60),
            )


def main():
    ap = argparse.ArgumentParser(description="Asteroid Salvage dev tuning console")
    ap.add_argument("--url", default="ws://localhost:8080/debug")
    ap.add_argument(
        "--check",
        action="store_true",
        help="validate the parameter table and exit without opening a window",
    )
    ap.add_argument(
        "--frames",
        type=int,
        default=0,
        help="render this many frames then exit; verifies the real draw path",
    )
    args = ap.parse_args()

    console = Console(args.url)

    if args.check:
        # No window: just prove the module imports, the client starts, and the parameter
        # table lines up with the protocol.
        assert len(proto.PARAMS) == len(proto.PARAM_DEFAULTS), "param table mismatch"
        for _pid, name, lo, hi in proto.PARAMS:
            default = proto.PARAM_DEFAULTS[name]
            assert lo <= default <= hi, f"{name} default {default} outside [{lo}, {hi}]"
        console.net.stop()
        print(f"OK: {len(proto.PARAMS)} tunables, all defaults within range")
        return

    if args.frames > 0:
        console.frame_limit = args.frames

    immapp.run(
        gui_function=console.draw,
        window_title="Asteroid Salvage — tuning",
        window_size=(560, 760),
        fps_idle=30,
    )
    console.net.stop()

    if args.frames > 0:
        print(f"OK: rendered {console.frames} frames without error")


if __name__ == "__main__":
    main()
