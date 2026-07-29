"""Asteroid Salvage client.

    python client/main.py
    python client/main.py --url ws://192.168.1.20:8080/ws --name Kev --team 2

The client renders and sends input. It runs no physics and predicts nothing: everything
on screen is server state, rendered ~110 ms in the past and interpolated (see interp.py).
On localhost that delay is imperceptible, and it keeps the client honest.
"""

from __future__ import annotations

import argparse
import math
import os
import sys

# Only when running from source. A frozen build has every module baked in and defines
# no __file__ for __main__, so touching it there is an immediate NameError crash.
if "__file__" in globals():
    sys.path.insert(0, os.path.dirname(__file__))
    sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "shared"))

from direct.showbase.ShowBase import ShowBase  # noqa: E402
from panda3d.core import Filename, WindowProperties, loadPrcFileData  # noqa: E402

import asteroid_protocol as proto  # noqa: E402
from audio import Audio  # noqa: E402
import controls as controls_mod  # noqa: E402
from hud import HUD  # noqa: E402
from interp import Interpolator  # noqa: E402
import cosmetics  # noqa: E402
from matchend import MatchEndSequence  # noqa: E402
from menu import Menus  # noqa: E402
from net import NetClient  # noqa: E402
from pause import PauseMenu  # noqa: E402
# Named playerprofile, not profile: the stdlib has a `profile` module (the profiler),
# and shadowing it is a coin toss in a frozen build's module resolution.
from playerprofile import Profile  # noqa: E402
from render import SceneRenderer  # noqa: E402
from shop import Shop  # noqa: E402

INPUT_HZ = 30.0


class Game(ShowBase):
    def __init__(self, args):
        loadPrcFileData("", "window-title Asteroid Salvage")
        loadPrcFileData("", "framebuffer-multisample 1")
        loadPrcFileData("", "multisamples 4")
        loadPrcFileData("", "sync-video 1")

        super().__init__()

        props = WindowProperties()
        props.setSize(1440, 900)
        self.win.requestProperties(props)

        self.disableMouse()  # the chase camera owns the view

        self.profile = Profile.load()
        self.scene = SceneRenderer(self, self._loadout_specs())
        self.hud = HUD(self)
        # Control hints belong to the game, not the menus — they showed through the
        # backdrop and made the front-end look unfinished.
        self.hud.set_help("")
        self.controls = controls_mod.Controls(self, sensitivity=args.sensitivity)
        self.controls.invert_y = args.invert_y

        # Mouse capture is deferred until a game is actually joined — the menu needs a
        # visible cursor to click buttons and a text field.

        self.interp = Interpolator()
        # The losing motherships' send-off. Owns its own overlay, so it survives across
        # matches without being rebuilt.
        self.matchend = MatchEndSequence(self, self.scene)
        self.net: NetClient | None = None
        self.shop: Shop | None = None
        self.menu: Menus | None = None
        # Kept past the menu being dismissed so a hosted server can still be shut down.
        self._server_owner: Menus | None = None
        self._args = args
        self._connecting = False

        # Shared with the settings screen so changes apply without a restart.
        self.settings = {
            "sensitivity": args.sensitivity,
            "invert_y": args.invert_y,
            "sfx_volume": args.sfx_volume,
            "music_volume": args.music_volume,
        }

        # Sounds are synthesised and cached on first run; this degrades to silence if
        # anything about that fails.
        self.audio = Audio(self, self.settings)

        self._input_accum = 0.0
        self._last_intent: dict = {}
        self._last_phase = None
        self._delivered_tier = None
        self._cargo_damaged = False
        self._own_ship = 0
        self._own_player = 0
        self._own_team = 0

        # Self-capture. Grabbing the desktop from outside is unreliable — it photographs
        # whatever window happens to be on top — so the client renders its own frame.
        self._shot_path = args.shot
        self._shot_at = args.shot_frames
        self._frames = 0

        # Demo mode flies itself using the same body-relative steering a player performs
        # with the mouse, so screenshots capture a real haul (arrow + tractor beam in
        # use) rather than a ship parked at spawn.
        self._demo = args.demo
        self._demo_fire = args.demo_fire
        self._demo_big = args.demo_big
        self._shot_hauling = args.shot_hauling
        self._shot_losers = args.shot_losers or args.shot_winner
        self._shot_winner = args.shot_winner
        self._shot_laser = args.shot_laser
        self._shot_boosting = args.shot_boosting

        # Mirrors the server's grab.range default. Only affects how far the searching
        # beam is drawn, so drifting out of sync is cosmetic rather than a desync.
        self.beam_range = args.beam_range
        self._shot_near = args.shot_near
        self._shot_pause = args.shot_pause
        self._shot_leave = args.shot_leave

        # ESC pauses rather than quits. Quitting outright on a stray keypress mid-match
        # is a hostile thing to do to someone who only wanted to check the controls.
        self.pause = PauseMenu(
            self, self.settings,
            on_resume=self._resume,
            on_leave=self._leave_game,
            on_exit=self.user_exit,
        )
        self.accept("escape", self._on_escape)
        self.taskMgr.add(self._tick, "client-tick")

        if args.url:
            # An explicit --url skips the menu, which keeps the automated tests and the
            # --demo autopilot working exactly as before.
            self._begin_connect(args.url)
        else:
            self.menu = Menus(self, self._begin_connect, self.settings, self.profile,
                              on_apply_loadout=self._apply_loadout)
            if args.shot_screen:
                self.menu.show(args.shot_screen)
            if args.host:
                # Straight into a solo game: start the bundled server and connect. Also
                # how the packaged build's hosting path gets exercised automatically.
                self.taskMgr.doMethodLater(0.2, self._auto_host, "auto-host")

    def _auto_host(self, task):
        if self.menu is not None:
            self.menu.show("host")
            self.menu._host()
        return task.done

    def _begin_connect(self, url: str) -> None:
        """Start a connection attempt. The menu stays up until it succeeds."""
        if self._connecting:
            return
        self._connecting = True

        self.net = NetClient(url, self._args.name, self._args.team)
        self.net.start()
        self.shop = Shop(self, self.net)
        self._connect_deadline = globalClock.getRealTime() + 6.0
        print(f"connecting to {url} as {self._args.name!r}")

    def _track_achievements(self, event, bodies) -> None:
        """Turn game events into profile stats, and surface anything they unlock.

        Tracked from events on the client rather than by the server: unlocks are purely
        cosmetic and personal, so this needs no protocol changes and cannot affect
        anyone else's match.
        """
        p = self.profile

        if event.type == proto.EVENT_DEPOSITED and event.player == self._own_player:
            p.add_stat("deliveries")
            p.add_stat("credits", event.value)

            # Tier is not on the event, so read it from the body we were hauling before
            # it was despawned — the interpolator still has the last frame it appeared in.
            tier = self._delivered_tier
            if tier == proto.TIER_CRYSTAL:
                p.add_stat("crystals")
            elif tier == proto.TIER_GOLD:
                p.add_stat("golds")
            elif tier == proto.TIER_MASSIVE:
                p.add_stat("massives")

            if not self._cargo_damaged:
                p.add_stat("pristine")
            self._cargo_damaged = False

        elif event.type == proto.EVENT_DAMAGED:
            self._cargo_damaged = True

        elif event.type == proto.EVENT_ROUND_END:
            if event.player == self._own_team:
                p.add_stat("round_wins")

        elif event.type == proto.EVENT_MATCH_OVER:
            if event.player == self._own_team:
                p.add_stat("match_wins")

        for ach in p.take_new_unlocks():
            self.hud.show_unlock(ach.name)
            p.save()

    def _react_to_event(self, event) -> None:
        """Turn combat events into things you can see and hear.

        Explosions are spawned from the event rather than from the snapshot, because the
        body is despawned server-side on the same tick it dies — by the time the next
        snapshot arrives there is nothing left to blow up.
        """
        if event.type == proto.EVENT_SHIP_DESTROYED:
            # Value carries the victim's team, and the entity is the wreck.
            self.scene.explode(event.entity, scale=3.5, shards=20)
            self.audio.play_explosion()

        elif event.type == proto.EVENT_ASTEROID_DESTROYED:
            tier = int(event.value)
            rgb = proto.TIER_COLORS.get(tier, (0.6, 0.55, 0.5))
            # Bigger tiers get a bigger send-off; a colossal coming apart should be the
            # most dramatic thing in the belt short of a mothership.
            scale = 2.6 if tier >= proto.TIER_MASSIVE else 1.6
            shards = 24 if tier >= proto.TIER_MASSIVE else 10
            self.scene.explode(event.entity, rgb=rgb, scale=scale, shards=shards)
            if tier >= proto.TIER_MASSIVE:
                self.audio.play_explosion()

        elif event.type == proto.EVENT_MOTHERSHIP_DESTROYED:
            # `value` carries the team whose station broke. The station is scenery the
            # server never despawns, so the wreck has to be hidden explicitly or the
            # debris flies out of a hull that is visibly still intact.
            self.scene.explode(event.entity, scale=2.2, shards=30)
            self.scene.breach_station(event.entity)
            self.audio.play_explosion()
            self.hud.show_toast(
                "YOUR STATION IS GONE" if int(event.value) == self._own_team
                else "STATION BREACHED"
            )

        elif event.type == proto.EVENT_MOTHERSHIP_HIT:
            # Only your own station raises an alarm. Being told about every hit anyone
            # lands on anyone would make the one that matters invisible.
            if self._own_team == self.scene.team_of(event.entity):
                self.hud.show_toast("HOME UNDER ATTACK")

        elif event.type == proto.EVENT_MATCH_OVER:
            # `player` carries the winning team id here, 0xFF on a draw.
            self.matchend.start(event.player, self._own_team, self.net.teams)
            self.audio.play_explosion()

    def _loadout_specs(self) -> dict:
        """Resolve the equipped cosmetic ids into the spec dicts the renderer wants."""
        return {
            slot: self.profile.equipped(slot).data
            for slot in cosmetics.SLOT_ORDER
        }

    def _apply_loadout(self, _profile) -> None:
        """Called when the ship editor applies. Rebuilds the ship you actually fly."""
        self.scene.set_loadout(self._loadout_specs())

    def _on_escape(self) -> None:
        """ESC: pause in-game, quit from the menus."""
        if self.menu is not None:
            self.user_exit()
            return
        self.pause.toggle()
        if not self.pause.visible:
            self._resume()

    def _resume(self) -> None:
        if self.controls.active:
            self.controls.capture_mouse()

    def _leave_game(self) -> None:
        """Disconnect, tear the world down, and go back to the main menu."""
        if self.net is not None:
            self.net.stop()
            self.net = None

        # Kill a server this client started, or the port stays held and hosting again
        # fails with nothing on screen to explain why.
        for owner in (self.menu, self._server_owner):
            if owner is not None:
                owner.stop_server()
        self._server_owner = None

        self.shop = None
        self.controls.active = False
        self.controls.release_mouse()
        self.audio.set_in_game(False)

        # Clear the world so the next game does not start with the last one's asteroids
        # frozen in place.
        self.matchend.stop()
        self.scene.clear()
        self.interp = Interpolator()
        self._own_ship = 0
        self._own_player = 0
        self._own_team = 0
        self._last_phase = None
        self._connecting = False
        self.hud.reset()
        self.hud.set_help("")

        self.menu = Menus(self, self._begin_connect, self.settings, self.profile,
                              on_apply_loadout=self._apply_loadout)

    def _sync_mouse_to_phase(self, match) -> None:
        """Hand the cursor to the shop during intermissions, take it back for flight.

        Only acts on a phase *change*, so pressing Tab to free the mouse mid-round is
        not immediately undone on the next frame.
        """
        if match is None:
            return

        phase = match.phase
        if phase == self._last_phase:
            return
        self._last_phase = phase

        if phase == proto.PHASE_INTERMISSION:
            self.controls.release_mouse()
        elif self.controls.active:
            self.controls.capture_mouse()

    def _await_connection(self) -> bool:
        """Keep the menu up until the server answers. Returns True once in-game.

        Waits for the Welcome rather than the socket opening: a socket that connects and
        then produces nothing looks identical to a working game from the player's side,
        which is exactly the confusion the menu exists to avoid.
        """
        if self.net.welcome is not None:
            # Hold on to the menu even after hiding it: it owns the server process this
            # client may have started, and dropping the reference here leaked a running
            # server every time the player quit — which then held port 8080 and broke
            # the next attempt to host.
            self._server_owner = self.menu

            # Apply anything chosen on the settings screen, and claim the team name the
            # host typed now that we have a player id to attach it to.
            self.controls.sensitivity = self.settings["sensitivity"]
            self.controls.invert_y = self.settings["invert_y"]
            if self.menu.team_name:
                self.net.set_team_name(self.menu.team_name)
            # Colour is a property of the team on the server, so it is claimed here
            # rather than passed as a launch flag — which makes it work when joining
            # someone else's game as well as when hosting one.
            colour = getattr(self.menu, "team_color", None)
            if colour is not None:
                self.net.set_team_color(colour)

            self.menu.hide()
            self.menu = None
            self.hud.set_help(controls_mod.HELP_TEXT)
            self.audio.set_in_game(True)

            # Flight controls come alive only now; until this point the cursor belongs
            # to the menus.
            self.controls.active = True
            self.controls.capture_mouse()
            return True

        if self.net.error is not None:
            self.menu.set_status(f"could not connect: {self.net.error}")
            self._reset_connection()
            return False

        if globalClock.getRealTime() > self._connect_deadline:
            self.menu.set_status("no response from that address - is the host running?")
            self._reset_connection()
            return False

        return False

    def _reset_connection(self) -> None:
        """Tear down a failed attempt so the player can try a different address."""
        if self.net is not None:
            self.net.stop()
            self.net = None
        self.shop = None
        self._connecting = False

    def _capture_mouse(self, task):
        self.controls.capture_mouse()
        return task.done

    def _tick(self, task):
        dt = globalClock.getDt()

        if self.net is None:
            # Still on the menu. Screenshots are handled here too so the menu itself can
            # be captured for verification.
            self.audio.set_in_game(False)
            if self.menu is not None:
                self.menu.update(dt)
            self._frames += 1
            if self._shot_path and self._frames >= self._shot_at:
                self._save_shot()
            return task.cont

        if self.menu is not None and not self._await_connection():
            return task.cont

        # Read and recentre the pointer before sampling intent.
        self.controls.update_mouse()

        for snap in self.net.take_snapshots():
            self.interp.add(snap)

        welcome = self.net.welcome
        if welcome is not None and self._own_ship == 0:
            self._own_ship = welcome.ship
            self._own_player = welcome.player_id
            self._own_team = welcome.team
            # The beam wears the crew's colour, so it has to wait until the server has
            # told us which crew that is.
            self.scene.set_beam_team(welcome.team)
            print(
                f"joined as player {welcome.player_id} on team {welcome.team} "
                f"(ship entity {welcome.ship})"
            )

        self.interp.advance(dt)
        bodies = self.interp.bodies()

        own_pos, mothership_pos, held = self._scan(bodies)
        # Remembered because a delivered rock is despawned before the event arrives, so
        # its tier cannot be looked up afterwards.
        if held is not None:
            self._delivered_tier = held.tier

        self.scene.sync(bodies, self._own_ship)
        # Show the beam whenever the button is down, so a missed grab is visibly a miss
        # rather than indistinguishable from a broken beam.
        beaming = self.controls.sample()["grab"] if not self._demo else True
        self.scene.update_guidance(
            self._own_ship,
            held.pos if held else None,
            dt,
            beaming=beaming,
            beam_range=self.beam_range,
        )
        self.scene.update_camera(self._own_ship, dt)
        self.scene.update_dust(self._own_ship)
        self.scene.update_shields(self.net.teams)

        # After the camera: the reticle is projected through it, so a frame-old camera
        # pose would leave the crosshair lagging every time the ship turns.
        player = self.net.player
        self.scene.update_reticle(
            self._own_ship,
            can_fire=player is None or player.energy >= 1.0,
            visible=player is None or not player.dead,
        )

        # Laser beams are drawn from the shooter's *current* pose, so they have to be
        # spawned after sync has moved the ships for this frame.
        for shot in self.net.take_shots():
            self.scene.fire_shot(shot)
            if shot.shooter == self._own_ship:
                self.audio.play_laser()
        self.scene.effects.update(dt)
        self.matchend.update(dt)

        # Input at a fixed rate rather than per frame: a 240 Hz machine should not get
        # eight times the input bandwidth of a 30 Hz one.
        self._input_accum += dt
        interval = 1.0 / INPUT_HZ
        if self._input_accum >= interval:
            self._input_accum %= interval
            if welcome is not None:
                if self._demo:
                    intent = self._demo_input(bodies, own_pos, mothership_pos, held)
                else:
                    intent = self.controls.sample()
                # Remembered so the boost roar and the bar follow what was actually
                # sent, rather than re-sampling the keyboard on a different frame.
                self._last_intent = intent
                self.net.send_input(**intent)

        for event in self.net.take_events():
            self.hud.show_event(event, self._own_player, self._own_ship)
            self._track_achievements(event, bodies)
            self._react_to_event(event)
            if (event.type == proto.EVENT_DEPOSITED
                    and event.player == self._own_player):
                self.audio.play_deposit()

        # Beam hum follows the cargo, not the button: it should sound when the beam has
        # actually caught something, not while you are waving it at empty space.
        self.audio.set_beam(held is not None)

        # Same principle for the boost roar — it follows the engines, not the key. The
        # server owns the tank, so an empty one means the key is down and nothing is
        # happening, which should be silent.
        boosting = self._is_boosting()
        self.audio.set_boost(boosting)

        self._update_hud(dt, own_pos, mothership_pos, held,
                         untowable=beaming and held is None
                         and self._aiming_at_untowable(bodies),
                         boosting=boosting,
                         stations={b.team: b.health for b in bodies
                                   if b.kind == proto.KIND_MOTHERSHIP},
                         hill=self._hill_status(bodies, own_pos))
        self.shop.update(self.net.match, self.net.player)
        self._sync_mouse_to_phase(self.net.match)

        self._frames += 1
        # --shot-hauling waits for cargo on the beam, so the captured frame shows the
        # mechanic rather than whatever the ship happened to be doing.
        ready = self._frames >= self._shot_at and (
            held is not None or not self._shot_hauling
        )
        if ready and self._shot_laser and self.scene.effects.bolts_live == 0:
            ready = False
        if ready and self._shot_boosting:
            # Wait for a frame where the tank is visibly part-spent, not full and not
            # empty — a full bar proves nothing about whether boost drains.
            ps = self.net.player
            if not self._is_boosting() or ps is None or ps.boost > ps.max_boost * 0.7:
                ready = False
        if ready and self._shot_near > 0:
            # Also wait until the mothership is close enough to actually be in shot.
            if own_pos is None or mothership_pos is None:
                ready = False
            else:
                ready = (
                    math.dist(
                        (own_pos.x, own_pos.y, own_pos.z),
                        (mothership_pos.x, mothership_pos.y, mothership_pos.z),
                    )
                    < self._shot_near
                )
        if self._shot_path and ready and self._shot_losers:
            # Fire the end-of-match sequence by hand and let it play far enough to
            # capture the payoff. Exercises the real code path — nothing here is a
            # mock-up of the overlay.
            if not self.matchend.active and self.matchend.banner.isHidden():
                # --shot-losers watches somebody else's station come apart;
                # --shot-winner is the same sequence from the winning seat, which is a
                # different overlay and worth being able to capture on its own.
                viewer = (self._own_team if self._shot_winner
                          else (self._own_team + 1) % 4)
                self.matchend.start(self._own_team, viewer, self.net.teams)
                return task.cont
            if self.matchend.banner.isHidden():
                return task.cont

        if self._shot_path and ready:
            if self._shot_pause and not self.pause.visible:
                # Open the pause overlay on the frame before capturing it.
                self.pause.show()
                return task.cont
            if self._shot_leave:
                # Exercise the full teardown, then capture whatever we land on — which
                # should be the main menu, not a crash or a frozen world.
                self._shot_leave = False
                self._leave_game()
                return task.cont
            self._save_shot()

        return task.cont

    def _save_shot(self) -> None:
        # graphicsEngine must finish the frame before the buffer is readable.
        self.graphicsEngine.renderFrame()
        ok = self.win.saveScreenshot(Filename.fromOsSpecific(self._shot_path))
        print(f"screenshot {'saved to ' + self._shot_path if ok else 'FAILED'}")
        self.user_exit()

    # A held rock closer to me than this is assumed to be mine. The snapshot marks a
    # body as held but not by whom, and cargo rides ~10-15 m off the nose, so anything
    # held within this radius can only be ours — another player's haul would have to be
    # flying alongside to fool it.
    OWN_CARGO_RADIUS = 40.0

    def _scan(self, bodies):
        """One pass over the world: my ship, MY mothership, and my cargo."""
        own_pos = None
        mothership_pos = None
        any_mothership = None
        best_held = None
        best_dist = self.OWN_CARGO_RADIUS

        for b in bodies:
            if b.entity == self._own_ship:
                own_pos = b.pos
            elif b.kind == proto.KIND_MOTHERSHIP:
                # Every team has its own hangar now, and only yours banks anything —
                # pointing the arrow at whichever one happened to be first in the
                # snapshot would send players to a rival's door with a full hold.
                if b.team == self._own_team:
                    mothership_pos = b.pos
                if any_mothership is None:
                    any_mothership = b.pos

        if mothership_pos is None:
            mothership_pos = any_mothership

        if own_pos is not None:
            for b in bodies:
                if not b.held or b.entity == self._own_ship:
                    continue
                d = math.dist(
                    (own_pos.x, own_pos.y, own_pos.z), (b.pos.x, b.pos.y, b.pos.z)
                )
                if d < best_dist:
                    best_dist, best_held = d, b

        return own_pos, mothership_pos, best_held

    # Mirrors the server's grabConeDot (server/sim/grab.go): roughly a 32 degree cone.
    GRAB_CONE_DOT = 0.85

    def _aiming_at_untowable(self, bodies) -> bool:
        """Is the player lining up a beam on a rock no beam can move?

        The server simply refuses the lock, which without this reads as a broken tractor
        beam rather than as a rule. Duplicating the cone test here is a small amount of
        drift risk in exchange for the player being told why nothing happened; it only
        drives a line of text, so being slightly off costs nothing.
        """
        ship = self.scene.nodes.get(self._own_ship)
        if ship is None:
            return False
        pos = ship.getPos()
        fwd = ship.getQuat().getForward()

        for b in bodies:
            if b.tier not in proto.TIER_UNTOWABLE:
                continue
            if b.kind not in (proto.KIND_ASTEROID, proto.KIND_SALVAGE):
                continue
            dx, dy, dz = b.pos.x - pos.x, b.pos.y - pos.y, b.pos.z - pos.z
            dist = math.sqrt(dx * dx + dy * dy + dz * dz)
            if dist < 1e-3 or dist > self.beam_range + b.radius:
                continue
            if (dx * fwd.x + dy * fwd.y + dz * fwd.z) / dist > self.GRAB_CONE_DOT:
                return True
        return False

    def _demo_input(self, bodies, own_pos, mothership_pos, held):
        """Autopilot for screenshots: hunt the nearest rock, then haul it home."""
        from playtest import steer_toward  # local import: only needed in demo mode

        ship = next((b for b in bodies if b.entity == self._own_ship), None)
        if ship is None or own_pos is None:
            return {"grab": True}

        if self._demo_big:
            # Hunt the biggest thing in the belt and shoot it apart. Exercises the late
            # round loop — the rocks nobody can tow — rather than the hauling one.
            bigs = [
                b
                for b in bodies
                if b.kind == proto.KIND_ASTEROID and b.tier in proto.TIER_UNTOWABLE
            ]
            if bigs:
                goal = min(
                    bigs,
                    key=lambda r: math.dist(
                        (own_pos.x, own_pos.y, own_pos.z), (r.pos.x, r.pos.y, r.pos.z)
                    ),
                ).pos
                cmd, dist = steer_toward(ship.pos, ship.rot, goal)
                # Stop short: flying into a 30 m rock just bounces you off it.
                if dist < 120:
                    cmd["thrust_fwd"] = 0.0
                    cmd["brake"] = True
                cmd["fire"] = True
                return cmd

        if held is not None and mothership_pos is not None:
            goal = mothership_pos
        else:
            rocks = [
                b
                for b in bodies
                if b.kind in (proto.KIND_ASTEROID, proto.KIND_SALVAGE)
                and not b.held
                # No beam can move these, so an autopilot that picks one just sits
                # there squeezing the trigger at a wall forever.
                and b.tier not in proto.TIER_UNTOWABLE
            ]
            if not rocks:
                return {"grab": True}
            goal = min(
                rocks,
                key=lambda r: math.dist(
                    (own_pos.x, own_pos.y, own_pos.z), (r.pos.x, r.pos.y, r.pos.z)
                ),
            ).pos

        cmd, dist = steer_toward(ship.pos, ship.rot, goal)
        cmd["grab"] = True
        # --demo-fire holds the trigger too, so a capture shows the laser actually
        # firing at something rather than a ship politely holding its fire.
        cmd["fire"] = self._demo_fire
        # Burn for the long legs. Exercises the tank draining and refilling, which is
        # what the capture modes need to show the boost bar doing anything.
        cmd["boost"] = dist > 150
        return cmd

    def _is_boosting(self) -> bool:
        """Are the engines actually running hot?

        Holding the key is not enough — the tank has to have something in it. The server
        is authoritative and reports what is left in PlayerState; combining that with the
        intent we last sent gives an answer that reacts on the frame the player presses,
        without ever roaring on an empty tank.
        """
        ps = self.net.player
        if ps is None or ps.dead:
            return False
        return bool(self._last_intent.get("boost")) and ps.boost > 0.02

    def _hill_status(self, bodies, own_pos):
        """The King of the Hill zone as the HUD wants it, or None in other modes.

        Read off the snapshot rather than from a mode flag: the hill only appears in the
        body list when the server is running that mode, so its presence *is* the check.
        """
        for b in bodies:
            if b.kind != proto.KIND_HAZARD:
                continue
            dist = None
            if own_pos is not None:
                dist = math.dist((own_pos.x, own_pos.y, own_pos.z),
                                 (b.pos.x, b.pos.y, b.pos.z))
            # Contested and empty both arrive as NO_TEAM. The server knows which, but the
            # snapshot has no room for it, so "somebody is in there and nobody owns it"
            # is inferred from a rival ship being inside the volume.
            contested = False
            if b.team == proto.NO_TEAM:
                for other in bodies:
                    if other.kind != proto.KIND_SHIP or other.dead:
                        continue
                    if math.dist((other.pos.x, other.pos.y, other.pos.z),
                                 (b.pos.x, b.pos.y, b.pos.z)) <= b.radius:
                        contested = True
                        break
            return {"team": b.team, "radius": b.radius, "distance": dist,
                    "contested": contested}
        return None

    def _update_hud(self, dt, own_pos, mothership_pos, held, untowable=False,
                    boosting=False, stations=None, hill=None):
        distance_home = None
        if own_pos is not None and mothership_pos is not None:
            distance_home = math.dist(
                (own_pos.x, own_pos.y, own_pos.z),
                (mothership_pos.x, mothership_pos.y, mothership_pos.z),
            )

        self.hud.update(
            dt=dt,
            connected=self.net.connected.is_set(),
            error=self.net.error,
            held_radius=held.radius if held else None,
            held_tier=held.tier if held else None,
            distance_home=distance_home,
            teams=self.net.teams,
            own_team=self._own_team,
            buffered=self.interp.buffered,
            match=self.net.match,
            player=self.net.player,
            untowable=untowable,
            boosting=boosting,
            stations=stations,
            hill=hill,
        )

    def user_exit(self):
        # Give the pointer back before the window goes away, or the cursor can stay
        # hidden on some drivers.
        self.controls.release_mouse()
        self.audio.stop_all()
        if self.net is not None:
            self.net.stop()
        # Kill a server this client started, so hosting never leaves an orphaned process
        # holding port 8080. Covers quitting from the menu and from in-game.
        for owner in (self.menu, self._server_owner):
            if owner is not None:
                owner.stop_server()
        super().userExit()


def main():
    ap = argparse.ArgumentParser(description="Asteroid Salvage client")
    ap.add_argument(
        "--url",
        default=None,
        help="connect straight to this server and skip the menu",
    )
    ap.add_argument("--name", default="pilot", help="player name (max 32 bytes)")
    ap.add_argument(
        "--team",
        type=lambda v: int(v, 0),
        default=0xFF,
        help="preferred team id, or 0xFF to auto-assign",
    )
    ap.add_argument(
        "--sensitivity",
        type=float,
        default=controls_mod.DEFAULT_SENSITIVITY,
        help="mouse look sensitivity",
    )
    ap.add_argument("--invert-y", action="store_true", help="invert mouse pitch")
    ap.add_argument("--sfx-volume", type=float, default=0.7, help="0..1")
    ap.add_argument("--music-volume", type=float, default=0.5, help="0..1")
    ap.add_argument(
        "--shot-menu",
        action="store_true",
        help="with --shot, capture the start menu instead of connecting",
    )
    ap.add_argument(
        "--shot-screen",
        default=None,
        choices=["main", "play", "host", "settings", "editor"],
        help="with --shot-menu, which menu screen to capture",
    )
    ap.add_argument(
        "--host",
        action="store_true",
        help="start the bundled server and play immediately, skipping the menu",
    )
    ap.add_argument(
        "--shot-pause",
        action="store_true",
        help="with --shot, open the pause overlay before capturing",
    )
    ap.add_argument(
        "--shot-leave",
        action="store_true",
        help="with --shot, leave the game first (exercises teardown back to the menu)",
    )
    ap.add_argument(
        "--beam-range",
        type=float,
        default=15.0,
        help="how far to draw the searching beam; match the server's grab.range",
    )
    ap.add_argument(
        "--shot",
        default=None,
        help="save a screenshot to this path and exit (for verifying visuals)",
    )
    ap.add_argument(
        "--shot-frames", type=int, default=150, help="frames to render before --shot"
    )
    ap.add_argument(
        "--demo",
        action="store_true",
        help="autopilot: hunt a rock and haul it home (for verifying visuals)",
    )
    ap.add_argument(
        "--shot-hauling",
        action="store_true",
        help="with --shot, wait until cargo is on the beam before capturing",
    )
    ap.add_argument(
        "--demo-fire",
        action="store_true",
        help="with --demo, hold the trigger as well (for verifying laser visuals)",
    )
    ap.add_argument(
        "--shot-winner",
        action="store_true",
        help="with --shot, capture the end-of-match overlay from the winning seat",
    )
    ap.add_argument(
        "--shot-losers",
        action="store_true",
        help="with --shot, play the end-of-match loser explosion before capturing",
    )
    ap.add_argument(
        "--shot-laser",
        action="store_true",
        help="with --shot, wait until a laser beam is on screen before capturing",
    )
    ap.add_argument(
        "--shot-boosting",
        action="store_true",
        help="with --shot, wait until the boost tank is part-spent before capturing",
    )
    ap.add_argument(
        "--demo-big",
        action="store_true",
        help="with --demo, hunt and shoot the rocks too big to tow instead of hauling",
    )
    ap.add_argument(
        "--shot-near",
        type=float,
        default=0.0,
        help="with --shot, wait until this close to the mothership before capturing",
    )
    args = ap.parse_args()

    # Automated modes must never sit on the menu waiting for a human to click —
    # unless the menu is the thing being captured.
    # --host starts a server of its own through the menu, so it must keep the menu path;
    # forcing a url here would skip it and then refuse to connect to a server nobody
    # started.
    if args.url is None and args.demo and not args.host:
        args.url = "ws://localhost:8080/ws"
    if args.url is None and args.shot and not args.shot_menu and not args.host:
        args.url = "ws://localhost:8080/ws"

    Game(args).run()


if __name__ == "__main__":
    main()
