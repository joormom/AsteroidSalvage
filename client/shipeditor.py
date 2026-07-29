"""Ship editor: pick cosmetics, see them on a rotating preview.

The preview is a real ship model rendered into a corner of the 3D scene rather than a
picture, so what you choose is literally what you fly — there is no second art pipeline
to drift out of sync.

Locked items are shown rather than hidden. Seeing "Crown — Win a match" is the whole
point; a hidden item gives a player no reason to chase it.
"""

from __future__ import annotations

import os
import sys

from direct.gui.DirectGui import DirectButton, DirectFrame, DirectLabel
from direct.gui.OnscreenImage import OnscreenImage
from panda3d.core import (
    AmbientLight,
    DirectionalLight,
    NodePath,
    TextNode,
    TransparencyAttrib,
    Vec4,
)

if "__file__" in globals():  # source runs only; frozen builds bake the module in
    sys.path.insert(0, os.path.dirname(__file__))

import cosmetics  # noqa: E402
from shipmodel import make_engine_glow, make_ship  # noqa: E402

TITLE_COLOR = (1.0, 0.85, 0.35, 1.0)
BODY_COLOR = (0.84, 0.88, 0.95, 1.0)
MUTED = (0.55, 0.58, 0.66, 1.0)
LOCKED = (0.62, 0.42, 0.40, 1.0)
GREY = (0.18, 0.20, 0.26, 1.0)
GREEN = (0.20, 0.45, 0.32, 1.0)


class ShipEditor:
    """One screen of the front-end menus. Owns its own preview node."""

    def __init__(self, base, parent, profile, on_back, on_apply=None):
        self.base = base
        self.profile = profile
        self.on_back = on_back
        # Called with the committed loadout so the game can rebuild its ship meshes.
        self.on_apply = on_apply

        # Browsing changes only this; the profile is written when Apply is pressed.
        # Committing on every click meant a stray tap through the whole list silently
        # rewrote your ship.
        self.pending: dict[str, str] = dict(profile.loadout)
        self.frame = DirectFrame(frameColor=(0, 0, 0, 0), parent=parent)
        self.frame.hide()

        self._spin = 0.0
        self._preview: NodePath | None = None

        DirectLabel(
            text="SHIP EDITOR", scale=0.085, pos=(0, 0, 0.80), text_fg=TITLE_COLOR,
            frameColor=(0, 0, 0, 0), parent=self.frame,
        )

        # The preview is rendered offscreen and shown as a texture.
        #
        # Two simpler approaches failed. A preview in the 3D world sits behind the menu
        # backdrop, which is a near-opaque 2D frame covering the screen. Parenting 3D
        # geometry into aspect2d puts it in front, but aspect2d strips lighting, so the
        # hull came out nearly black whatever light state was forced onto it.
        #
        # An offscreen buffer with its own camera and lights sidesteps both: it renders
        # a properly lit ship into a texture with a transparent background, which then
        # composites over the backdrop like any other GUI image.
        self._buffer = base.win.makeTextureBuffer("ship-preview", 640, 640)
        self._buffer.setClearColor(Vec4(0, 0, 0, 0))
        self._buffer.setSort(-100)  # render before the main window each frame

        self.stage = NodePath("ship-editor-stage")
        self._cam = base.makeCamera(self._buffer)
        self._cam.reparentTo(self.stage)
        self._cam.setPos(0, -7.2, 1.5)
        self._cam.lookAt(0, 0, -0.1)
        self._light_stage()

        self.preview_image = OnscreenImage(
            image=self._buffer.getTexture(),
            pos=(0.62, 0, 0.02),
            scale=0.42,
            parent=self.frame,
        )
        self.preview_image.setTransparency(TransparencyAttrib.MAlpha)

        self.rows: dict[str, dict] = {}
        y = 0.52
        for slot in cosmetics.SLOT_ORDER:
            self.rows[slot] = self._build_row(slot, y)
            y -= 0.30

        self.hint = DirectLabel(
            text="", scale=0.042, pos=(-0.92, 0, -0.60), text_fg=MUTED,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=self.frame,
        )
        self.status = DirectLabel(
            text="", scale=0.044, pos=(-0.92, 0, -0.68), text_fg=MUTED,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=self.frame,
        )

        self.apply_button = DirectButton(
            text="APPLY", scale=0.07, pos=(-0.30, 0, -0.84), frameColor=GREEN,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.42, 0.13),
            command=self._apply, parent=self.frame,
        )
        DirectButton(
            text="BACK", scale=0.07, pos=(0.34, 0, -0.84), frameColor=GREY,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.45, 0.13),
            command=self._back, parent=self.frame,
        )

        self._refresh_all()

    # --- construction ------------------------------------------------------

    def _light_stage(self) -> None:
        """Light the offscreen scene the same way the game lights ships."""
        amb = AmbientLight("editor-amb")
        amb.setColor(Vec4(0.52, 0.53, 0.58, 1.0))
        self.stage.setLight(self.stage.attachNewNode(amb))

        key = DirectionalLight("editor-key")
        key.setColor(Vec4(0.9, 0.86, 0.78, 1.0))
        knp = self.stage.attachNewNode(key)
        knp.setHpr(35, -40, 0)
        self.stage.setLight(knp)

        fill = DirectionalLight("editor-fill")
        fill.setColor(Vec4(0.34, 0.36, 0.42, 1.0))
        fnp = self.stage.attachNewNode(fill)
        fnp.setHpr(-150, 30, 0)
        self.stage.setLight(fnp)

    def _build_row(self, slot: str, y: float) -> dict:
        DirectLabel(
            text=cosmetics.SLOT_LABELS[slot], scale=0.05,
            pos=(-0.92, 0, y + 0.09), text_fg=BODY_COLOR, text_align=TextNode.ALeft,
            frameColor=(0, 0, 0, 0), parent=self.frame,
        )
        DirectButton(
            text="<", scale=0.052, pos=(-0.86, 0, y - 0.02), frameColor=GREY,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.22, 0.10),
            command=self._step, extraArgs=[slot, -1], parent=self.frame,
        )
        name = DirectLabel(
            text="", scale=0.052, pos=(-0.74, 0, y - 0.02), text_fg=TITLE_COLOR,
            text_align=TextNode.ALeft, frameColor=(0, 0, 0, 0), parent=self.frame,
        )
        DirectButton(
            text=">", scale=0.052, pos=(-0.14, 0, y - 0.02), frameColor=GREY,
            text_fg=(1, 1, 1, 1), relief=1, pad=(0.22, 0.10),
            command=self._step, extraArgs=[slot, 1], parent=self.frame,
        )
        return {"name": name, "index": self._equipped_index(slot)}

    def _equipped_index(self, slot: str) -> int:
        current = self.pending.get(slot, self.profile.loadout.get(slot, ""))
        for i, c in enumerate(cosmetics.SLOTS[slot]):
            if c.id == current:
                return i
        return 0

    # --- interaction -------------------------------------------------------

    def _step(self, slot: str, delta: int) -> None:
        options = cosmetics.SLOTS[slot]
        row = self.rows[slot]
        row["index"] = (row["index"] + delta) % len(options)

        item = options[row["index"]]
        if self.profile.is_unlocked(item):
            self.pending[slot] = item.id

        self._refresh_all()

    def _apply(self) -> None:
        for slot, cosmetic_id in self.pending.items():
            self.profile.equip(slot, cosmetic_id)
        self.profile.save()

        if self.on_apply is not None:
            self.on_apply(self.profile)

        self.status.setText("Applied - your ship is updated.")
        self.status["text_fg"] = (0.5, 1.0, 0.6, 1.0)
        self._refresh_all()

    def _dirty(self) -> bool:
        return any(
            self.pending.get(slot) != self.profile.loadout.get(slot)
            for slot in cosmetics.SLOT_ORDER
        )

    def _refresh_all(self) -> None:
        locked_hint = ""
        for slot in cosmetics.SLOT_ORDER:
            options = cosmetics.SLOTS[slot]
            row = self.rows[slot]
            item = options[row["index"]]
            unlocked = self.profile.is_unlocked(item)

            suffix = "" if unlocked else "   (locked)"
            row["name"].setText(f"{item.name}{suffix}")
            row["name"]["text_fg"] = TITLE_COLOR if unlocked else LOCKED

            if not unlocked and not locked_hint:
                locked_hint = f"{item.name}: {cosmetics.unlock_hint(item)}"

        self.hint.setText(locked_hint or "Press APPLY to fly this loadout.")

        # Nothing to apply is worth showing plainly, so the button is never a mystery.
        dirty = self._dirty()
        self.apply_button["frameColor"] = GREEN if dirty else (0.16, 0.20, 0.18, 1.0)
        if dirty:
            self.status.setText("unsaved changes")
            self.status["text_fg"] = (1.0, 0.78, 0.35, 1.0)

        self._rebuild_preview()

    def _rebuild_preview(self) -> None:
        if self._preview is not None:
            self._preview.removeNode()

        # Preview the PENDING selection, not the saved one — the whole point of a
        # preview is seeing a change before committing it. Locked items never enter
        # `pending`, so this cannot show you wearing something unearned.
        booster = cosmetics.get("booster", self.pending.get("booster", "")).data
        hat = cosmetics.get("hat", self.pending.get("hat", "")).data
        exhaust = cosmetics.get("exhaust", self.pending.get("exhaust", "")).data

        ship = make_ship(self.base.loader, booster=booster, hat=hat)
        make_engine_glow(booster=booster, exhaust=exhaust).reparentTo(ship)

        # Centred in the offscreen scene; the camera frames it.
        ship.reparentTo(self.stage)
        ship.setPos(0, 0, 0)
        self._preview = ship

    # --- lifecycle ---------------------------------------------------------

    def show(self) -> None:
        self.frame.show()
        # Re-read the profile: an achievement may have unlocked since last time, and any
        # unapplied browsing from a previous visit should be discarded rather than
        # silently resurface.
        self.pending = dict(self.profile.loadout)
        self.status.setText("")
        for slot in cosmetics.SLOT_ORDER:
            self.rows[slot]["index"] = self._equipped_index(slot)
        self._refresh_all()

    def hide(self) -> None:
        # The stage is a child of the frame, so hiding the frame hides it too.
        self.frame.hide()

    def update(self, dt: float) -> None:
        """Spin the preview so the hat and boosters are both visible over time."""
        if self._preview is None or self.frame.isHidden():
            return
        self._spin = (self._spin + dt * 28.0) % 360.0
        self._preview.setHpr(self._spin, -12.0, 0.0)

    def _back(self) -> None:
        self.hide()
        self.on_back()

    def destroy(self) -> None:
        if self._preview is not None:
            self._preview.removeNode()
        self._cam.removeNode()
        self.stage.removeNode()
        # Offscreen buffers are engine-owned; leaking one per editor teardown would
        # accumulate GPU memory across Leave Game cycles.
        self.base.graphicsEngine.removeWindow(self._buffer)
        self.frame.destroy()
