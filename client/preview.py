"""Render a model on its own and save a PNG.

    python client/preview.py --model mothership --out bin/ms.png
    python client/preview.py --model ship --out bin/ship.png --hpr 210 -20 0

Inspecting a model through the game camera is unreliable: it is wherever the flight
happens to put it, usually partly off-screen. This frames the model in isolation with
the same lighting the game uses, which is the only way to actually judge a silhouette.
"""

from __future__ import annotations

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(__file__))

from direct.showbase.ShowBase import ShowBase  # noqa: E402
from panda3d.core import (  # noqa: E402
    AmbientLight,
    DirectionalLight,
    Filename,
    Vec4,
    loadPrcFileData,
)

import shipmodel  # noqa: E402


class Preview(ShowBase):
    def __init__(self, args):
        loadPrcFileData("", "window-title Model preview")
        loadPrcFileData("", "framebuffer-multisample 1")
        loadPrcFileData("", "multisamples 4")
        super().__init__()

        self.setBackgroundColor(0.05, 0.06, 0.09, 1.0)
        self.disableMouse()

        # Same three-light rig as render.py, so what you see here is what the game shows.
        ambient = AmbientLight("ambient")
        ambient.setColor(Vec4(0.52, 0.53, 0.58, 1.0))
        self.render.setLight(self.render.attachNewNode(ambient))

        key = DirectionalLight("key")
        key.setColor(Vec4(0.85, 0.82, 0.75, 1.0))
        knp = self.render.attachNewNode(key)
        knp.setHpr(35, -45, 0)
        self.render.setLight(knp)

        fill = DirectionalLight("fill")
        fill.setColor(Vec4(0.34, 0.36, 0.42, 1.0))
        fnp = self.render.attachNewNode(fill)
        fnp.setHpr(-160, 35, 0)
        self.render.setLight(fnp)

        if args.model == "sphere":
            model = shipmodel.make_sphere(1)
            model.setColor(0.6, 0.6, 0.65, 1)
        elif args.model == "mothership":
            model = shipmodel.make_mothership(self.loader)
        else:
            model = shipmodel.make_ship(self.loader)
            shipmodel.make_engine_glow().reparentTo(model)

        model.reparentTo(self.render)
        model.setHpr(*args.hpr)

        # Frame the model from its actual bounds rather than a guessed distance.
        lo, hi = model.getTightBounds()
        size = max(hi[0] - lo[0], hi[1] - lo[1], hi[2] - lo[2])
        centre = (lo + hi) * 0.5
        dist = size * args.zoom

        self.camera.setPos(centre[0] + dist * 0.75, centre[1] - dist, centre[2] + dist * 0.5)
        self.camera.lookAt(centre[0], centre[1], centre[2])

        self._frames = 0
        self._out = args.out
        self.taskMgr.add(self._tick, "preview")

    def _tick(self, task):
        self._frames += 1
        if self._frames >= 30:
            self.graphicsEngine.renderFrame()
            ok = self.win.saveScreenshot(Filename.fromOsSpecific(self._out))
            print(f"{'saved ' + self._out if ok else 'SAVE FAILED'}")
            self.userExit()
        return task.cont


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--model", choices=["ship", "mothership", "sphere"], default="mothership"
    )
    ap.add_argument("--out", default="bin/preview.png")
    ap.add_argument("--zoom", type=float, default=1.6, help="camera distance / model size")
    ap.add_argument("--hpr", type=float, nargs=3, default=[210.0, -15.0, 0.0])
    args = ap.parse_args()
    Preview(args).run()


if __name__ == "__main__":
    main()
