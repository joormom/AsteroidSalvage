# Wire Protocol — v1

**This file is the single source of truth.** Go (`server/net/encode.go`) and Python
(`client/net.py`, `tools/devconsole/debugnet.py`) each implement it independently, so
protocol drift is the most likely source of bugs in this project. Change this document
*before* changing either implementation.

## Framing

- Transport: WebSocket **binary** frames (never text — JSON in the hot path would waste
  sprocket's zero-allocation design).
- Byte order: **little-endian** throughout.
- Every message: 1-byte type tag, then a type-specific payload.
- One message per frame. No length prefix needed — WebSocket already frames.

Types `0x01–0x7F` are client→server. Types `0x80–0xFF` are server→client.

## Coordinates & units

Right-handed, Z-up (Panda3D's native convention, so the client needs no axis swizzle).
Metres, kilograms, seconds. Ship-local axes: +Y forward, +X right, +Z up.

## Rates

| Channel | Rate |
|---|---|
| Simulation tick | 30 Hz (fixed, `lg_world_step`) |
| Snapshot broadcast | 15 Hz (every 2nd tick) |
| Client input | 30 Hz |
| TeamState | 2 Hz |
| DebugStats | ~4 Hz |

Snapshots are every 2nd tick rather than a nominal 20 Hz because 20 Hz off a 30 Hz loop
needs 1.5 ticks, which in practice alternates 33 ms / 67 ms. Uneven spacing is worse for
interpolation than a slightly lower but even rate, and the client buffers ~100 ms
regardless. Raise to every tick (30 Hz) if bandwidth ever proves cheaper than latency.

---

## Client → Server

### `0x01` Input

Sent at 30 Hz, unreliable-in-spirit (latest wins; `seq` lets the server drop reorders).

| Field | Type | Notes |
|---|---|---|
| `seq` | `u32` | Monotonic per client. Server ignores `seq <= last_seq`. |
| `thrust_fwd` | `f32` | −1..1 |
| `thrust_right` | `f32` | −1..1 |
| `thrust_up` | `f32` | −1..1 |
| `yaw` | `f32` | −1..1 |
| `pitch` | `f32` | −1..1 |
| `roll` | `f32` | −1..1 |
| `flags` | `u8` | bit0 = grab held, bit1 = boost, bit2 = brake, bit3 = fire |

Size: 1 + 4 + 24 + 1 = **30 bytes**.

### `0x02` Hello

First message after connect.

| Field | Type | Notes |
|---|---|---|
| `name_len` | `u8` | ≤ 32 |
| `name` | `u8[name_len]` | UTF-8 |
| `team_pref` | `u8` | `0xFF` = no preference / auto-assign |

### `0x03` BuyUpgrade

| Field | Type | Notes |
|---|---|---|
| `upgrade` | `u8` | `0`=tractor amplifier, `1`=engine boost, `2`=beam extender, `3`=cargo dampeners, `4`=laser focuser, `5`=capacitor bank, `6`=fast recharger |

Rejected outside `PhaseIntermission`. On success the server replies with a fresh
`0x85 PlayerState`. Costs and effects live in `server/sim/upgrades.go`.

### `0x10` DebugSet — dev builds only

Rejected unless the server was started with `--dev`. **This is a remote
physics-mutation endpoint**; it must never be reachable in a release build.

| Field | Type | Notes |
|---|---|---|
| `param` | `u16` | See parameter IDs below |
| `value` | `f32` | |

---

## Server → Client

### `0x80` Welcome

Sent once, immediately after `Hello`.

| Field | Type |
|---|---|
| `player_id` | `u32` |
| `ship_entity` | `u32` |
| `team_id` | `u8` |
| `tick_rate` | `u8` |
| `snapshot_rate` | `u8` |

### `0x81` Snapshot

Broadcast to the match room at 20 Hz.

| Field | Type | Notes |
|---|---|---|
| `tick` | `u32` | Server sim tick |
| `server_ms` | `u32` | Server clock, for interpolation delay |
| `count` | `u16` | Number of body records following |

Then `count` records of **27 bytes** each:

| Field | Type | Notes |
|---|---|---|
| `entity` | `u32` | |
| `kind` | `u8` | `0`=ship, `1`=asteroid, `2`=salvage, `3`=mothership, `4`=hazard |
| `flags` | `u8` | bit0 = held, bit1 = sleeping, bit2 = damaged, bit3 = dead (destroyed ship awaiting respawn — do not draw) |
| `tier` | `u8` | `0`=rubble, `1`=iron, `2`=crystal, `3`=gold, `4`=massive, `5`=colossal, `6`=core, `7`=titan |
| `team` | `u8` | Owning crew for ships and motherships; `0xFF` = neutral |
| `health` | `u8` | Fraction of this body's maximum, `value / 255`. Always 255 for anything that cannot be shot |
| `radius` | `u16` | Metres in 0.05 m units (`radius_m = value * 0.05`), so 0–3276 m |
| `pos` | `f32[3]` | |
| `rot` | `u32` | Quaternion, smallest-three packed (see below) |

Radius was one byte at 0.25 m units until titan-class asteroids grew past its 63.75 m
ceiling. Two bytes at 0.05 m costs one byte per body — about 3 KB/s at full load — and
removes the ceiling as a design constraint.

`tier` drives the client's asteroid colour and the "needs a second beam" warning. The
balance table it refers to (size, density, payout, spawn weight, toughness and what it
splits into) lives in `server/sim/tiers.go` — only the tier id is on the wire.

`team` travels with every body rather than in a side table because the client colours
ships and motherships by team, and a body can appear in a snapshot before any other
message has said who owns it. Each team has its own mothership, and a deposit only
counts at your own.

Radius is resent every snapshot rather than announced once at spawn. It is static per
entity, so this is redundant — but at one byte it costs ~3 KB/s at full load, and it
means a client joining mid-match needs no separate state resync to draw the world.

**Smallest-three quaternion packing:** drop the largest-magnitude component (it is
recoverable as `sqrt(1 - a² - b² - c²)`), store its index in the top 2 bits, then the
remaining three components each quantised to 10 bits over the range ±1/√2. Costs 4 bytes
instead of 16, with ~0.1° error — far below what is visible.

Budget: 200 bodies → ~4.6 KB/snapshot → ~69 KB/s per client at 15 Hz. Fine for LAN and
localhost.
Delta compression and interest management are deferred to Phase 6 and only if profiling
demands them (libspatial's octree already supports the radius query interest management
would need).

### `0x82` Event

Discrete, non-interpolated occurrences. Drives sound and HUD feedback.

| Field | Type | Notes |
|---|---|---|
| `event` | `u8` | See the table below |
| `entity` | `u32` | Subject |
| `player` | `u32` | Actor, `0` if none |
| `value` | `f32` | Deposited value / damage magnitude / see below |

| id | event | `player` | `value` |
|---|---|---|---|
| `0` | grabbed | actor | — |
| `1` | dropped | actor | — |
| `2` | deposited | actor | credits banked |
| `3` | damaged | actor | damage |
| `4` | destroyed (cargo worthless) | — | `0` |
| `5` | round start | — | round number |
| `6` | round end | **team id**, `0xFF` = draw | winning score |
| `7` | match over | **team id**, `0xFF` = draw | round wins |
| `8` | ship hit | shooter | damage |
| `9` | ship destroyed | killer | victim's team |
| `10` | ship respawn | — | team |
| `11` | asteroid hit | shooter | damage |
| `12` | asteroid destroyed | shooter | tier that broke |
| `13` | mothership hit | shooter | damage actually taken |
| `14` | mothership destroyed | shooter | **team id** whose station broke |
| `15` | shield absorbed | shooter | damage the shield ate |
| `16` | team eliminated | — | **team id** of the last crew standing |

For `5`–`7` and `9`–`10` the `player` field carries a **team id**, not a player id.

A shielded station emits `15` immediately before the `13` for the same hit, so a client
can flare the bubble instead of sparking the hull. A client that does not know about
shields still sees a normal hit and draws something sensible. Event `14` ends the round on
the spot — see `endRoundByBreach` — so a client will see the phase change to intermission
in the same breath.

### `0x83` TeamState

| Field | Type | Notes |
|---|---|---|
| `team_count` | `u8` | |
| per team | `u8` `team_id`, `f32` `score`, `u8` `member_count`, `u8` `name_len`, `u8[name_len]` `name`, `u8` `shields`, `u8` `turrets`, `u8` `lives` | |

Records are variable width because team names are length-prefixed (≤ 20 bytes).

In co-op mode `team_count` is 1 and every player is in it.

`shields` and `turrets` are the team's **station** upgrade levels. They travel here rather
than in `0x85 PlayerState` because they are shared by the whole crew and because a client
needs a *rival's* shield level to draw their bubble, not only its own. Every other
upgrade is personal and stays in `PlayerState`.

`lives` is the crew's remaining respawns this round, reset when a round begins. It is
broadcast for every team on purpose: knowing a rival is down to its last ship is what
makes pressing an attack a decision rather than a guess. A team on zero lives with nobody
alive is out for the round, and when only one crew is left the round ends — see
`endRoundByElimination`.

### `0x84` MatchState

Broadcast at 2 Hz. Drives the round banner, the clock and the shop's visibility.

| Field | Type | Notes |
|---|---|---|
| `phase` | `u8` | `0`=warmup, `1`=round, `2`=intermission, `3`=match over |
| `round` | `u8` | 1-based once play starts |
| `best_of` | `u8` | |
| `wins_needed` | `u8` | majority of `best_of` |
| `time_left` | `u16` | seconds remaining in this phase |
| `winner` | `u8` | team id, `0xFF` = none or draw |
| `team_count` | `u8` | |
| per team | `u8` `team_id`, `u8` `round_wins` | |

### `0x85` PlayerState

Sent **per session**, not broadcast — credits and upgrades are personal and a player
must not see what rivals can afford.

| Field | Type | Notes |
|---|---|---|
| `credits` | `f32` | |
| `upgrade_count` | `u8` | |
| per upgrade | `u8` `id`, `u8` `level` | |
| `energy` | `f32` | Current weapon charge, in shots |
| `max_energy` | `f32` | Magazine size, including the capacitor upgrade |
| `health` | `f32` | Hull, 0–1 |
| `respawn_in` | `f32` | Seconds until respawn; `0` when alive |
| `boost` | `f32` | Seconds of burn left in the tank |
| `max_boost` | `f32` | Tank size |
| `boost_cooldown` | `f32` | Seconds of forced stall after running dry; `0` normally |
| `grounded` | `u8` | `1` when the team's life pool is spent and no respawn is coming |

The trailing blocks are appended rather than inserted, so a decoder that stops at the end
of the length-prefixed upgrade table — or at the end of the combat block — still parses
the part it understands.

Sent at the snapshot rate, not the 2 Hz shop cadence — `energy`, `health` and `boost`
drive live HUD bars.

Boost is a tank, not a modifier: the client sends `flags` bit1 while the key is held, and
the server decides whether the engines actually run hot. A client must not assume that
holding the key means boosting — it reports `boost` for exactly this reason, and the
sound and the bar should follow the tank rather than the key.

`boost_cooldown` is non-zero only after the tank has been run all the way down. While it
counts, the tank does not refill and no burn can start at any charge level, so `boost` and
`boost_cooldown` are genuinely different states and should not be drawn the same way. A
client should also not treat a small non-zero `boost` as usable: the server requires a
floor (`sim.BoostReengageSeconds`) to *start* a burn, as opposed to continuing one.

### `0x86` ShopOffers

Sent per session at 2 Hz. This intermission's randomised choices; `0x04 BuyOffer` picks
one by slot, so a client cannot buy something it was never shown.

| Field | Type |
|---|---|
| `count` | `u8` |
| per offer | `u8` `upgrade`, `u8` `level`, `f32` `cost` |

### `0x87` Shots

Broadcast every tick a laser is fired, not on the snapshot cadence: hitscan beams exist
for exactly one tick, and a dropped broadcast means the beam is never drawn at all.

| Field | Type | Notes |
|---|---|---|
| `count` | `u8` | |

Then `count` records of **14 bytes** each:

| Field | Type | Notes |
|---|---|---|
| `shooter` | `u32` | Entity id of the firing ship, or of a mothership for a turret |
| `team` | `u8` | Colours the beam |
| `flags` | `u8` | bit0 = the shot connected |
| `length` | `f32` | Metres the beam travelled before stopping |
| `target` | `u32` | What it connected with; `0` on a miss |

The client reconstructs the beam from the shooter's *current* interpolated pose, because a
beam that starts at the ship you can see beats one that starts where that ship was 110 ms
ago.

`target` exists for mothership turrets. A ship's aim can be read off its own rotation; a
station is static and its rotation says nothing about where its guns were pointing, so
without a target on the wire every turret beam would be drawn along the same arbitrary
axis. Clients should aim at the target's live position when there is one and fall back to
the shooter's nose when there is not.

A shot that hits nothing carries the distance at which it left the play volume — a
sphere of radius `sim.MapRadius` centred on the origin — rather than a fixed maximum
range. So `length` on a miss varies with where the shooter was standing and which way
they were pointing, and can be anything up to the map's full diameter.

### `0x90` DebugStats — dev builds only

| Field | Type |
|---|---|
| `tick_ms` | `f32` |
| `body_count` | `u16` |
| `session_count` | `u16` |

---

## Debug parameter IDs

Used by `0x10 DebugSet`. Mirrored in `config/feel.toml` so a tuned feel is reproducible
rather than rediscovered.

| ID | Name | Default | Range |
|---|---|---|---|
| `0x0001` | `grab.spring` | 220.0 | 0–2000 |
| `0x0002` | `grab.damping` | 28.0 | 0–200 |
| `0x0003` | `grab.max_force` | 1800.0 | 0–20000 |
| `0x0004` | `grab.hold_distance` | 6.0 | 1–30 |
| `0x0005` | `grab.reaction_scale` | 1.0 | 0–2 |
| `0x0006` | `grab.range` | 15.0 | 5–200 |
| `0x0010` | `ship.thrust` | 900.0 | 0–5000 |
| `0x0011` | `ship.torque` | 1400.0 | 0–6000 |
| `0x0012` | `ship.linear_damping` | 0.35 | 0–5 |
| `0x0013` | `ship.angular_damping` | 900.0 | 0–3000 |

`ship.torque` and `ship.angular_damping` are best tuned through what they produce rather
than directly: top turn rate is `torque / angular_damping`, and responsiveness is
`inertia / angular_damping` where the ship's inertia is `0.4·m·r²` = 192. Defaults give
~1.55 rad/s and a 0.21 s time constant.
| `0x0020` | `salvage.damage_threshold` | 6.0 | 0–50 |
| `0x0021` | `salvage.damage_scale` | 0.04 | 0–1 |

`grab.reaction_scale` is the single most important value in the game: it scales the
equal-and-opposite force the held mass exerts back on the ship, and is what makes a heavy
asteroid *feel* heavy. `1.0` is physically correct; the tuned value may not be.
