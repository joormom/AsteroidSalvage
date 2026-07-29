# Asteroid Salvage

A 3D multiplayer space hauling game. Fly out from a mothership, grab asteroids, and haul
them home without smashing them. Built for 16 players in teams of 4, scalable down to
two, with a co-op mode where everyone shares one score pool.

The design reference is **R.E.P.O.** — its *feel*, not its content. Mass, momentum and
fragility are the whole game: you grab a thing, it fights you, and getting it home intact
is the challenge.

## Architecture

| Layer | Language | Why |
|---|---|---|
| Physics | C11 ([lagrange](https://github.com/kevinfling/lagrange) + [libspatial](https://github.com/kevinfling/libspatial)) | Rigid bodies, forces, collision response |
| Simulation + netcode | Go, cgo to lagrange, [sprocket](https://github.com/kevinfling/sprocket) WebSockets | Authoritative rules, rooms map onto matches and teams |
| Client | Python + Panda3D | Renders and sends input. No physics, no prediction |
| Dev console | Python + [Dear ImGui](https://github.com/ocornut/imgui) via `imgui-bundle` | Live tuning of how hauling feels |

The server owns everything. The client draws server state ~110 ms in the past and
interpolates between snapshots; on localhost that delay is imperceptible and it keeps the
client honest.

```
client/ (Panda3D) ──input 30 Hz──►  server/ (Go)  ──cgo──►  vendor/lagrange (C)
                  ◄─snapshots 15 Hz─┘   │
tools/devconsole/ ◄──DebugStats─────────┘
                  ──DebugSet──────────►
```

## Setup

Requires a **GCC-compatible** compiler — cgo cannot use MSVC.

```bash
winget install GoLang.Go
winget install BrechtSanders.WinLibs.POSIX.UCRT   # MinGW-w64, NOT Visual Studio
pip install panda3d numpy imgui-bundle websockets

git submodule update --init --recursive           # pulls lagrange + libspatial
```

Verify: `gcc --version`, `go version`, `python -c "import panda3d"`.

## Sharing it

```bash
python build_dist.py
```

Produces `dist/AsteroidSalvage-win64.zip` (~34 MB). Send that to anyone on Windows 10 or
later — they unzip it and run `AsteroidSalvage.exe`. **Nothing to install**: no Python,
no Go, no toolchain.

One player clicks HOST A GAME (which launches the bundled `server.exe` and shows their
LAN address); everyone else types that address and clicks JOIN. `--host` skips the menu
and goes straight into a solo game.

The build needs Go, MinGW-w64 and Python on *your* machine — not on theirs. Override
toolchain locations with the `GO_BIN` and `MINGW_BIN` environment variables.

Note the game is asset-free by design: every model is generated in code. Panda3D's
bundled models are not carried into a frozen build, and depending on them meant the
packaged game died on a missing `models/misc/sphere`.

## Running

**Double-click `play.bat`.** It builds the server, then opens the game and the tuning
console. Click HOST A GAME to start a match with your chosen settings; closing the game
shuts the server down again, and the console reconnects on its own.

Or by hand:

```bash
cd server && go run .                 # server + dev console endpoint on :8080
python client/main.py                 # the game
python tools/devconsole/main.py       # tuning sliders
```

Server flags: `-addr :8080`, `-teams 4`, `-teamsize 4`, `-coop 1`, `-seed N`, `-dev=false`.

### Controls

| Input | Action |
|---|---|
| `W` / `S` | thrust forward / back |
| `A` / `D` | strafe left / right |
| **Mouse** | aim (there is no roll — point where you want to go and burn) |
| **Hold right-click** | tractor beam: lock on and reel in |
| **Left-click** | fire the laser — the ringed crosshair shows where it goes |
| `Shift` / `Space` | boost / brake |
| `Tab` | release the mouse |
| `Esc` | pause (Resume / Settings / Leave Game / Exit Game) |

## Ship editor and achievements

The main menu's **Ship Editor** customises your ship: boosters, hats, booster effects and
tractor-beam colours. Most are locked behind achievements ("Win a match", "Deliver a
MASSIVE asteroid"), shown greyed out with the requirement rather than hidden — an item
you can't see gives you no reason to chase it.

A crew also picks a **team colour from sixteen**, on the host screen. Colour is a property
of the team on the server rather than a launch flag, so it applies when joining someone
else's game too, and everything that draws in team colours — hulls, stations, lasers, the
tractor beam, the end-of-match clip — follows from one shared mapping.

**Boosting is visible from outside the ship.** The plume rides on the snapshot as a body
flag, so every client draws every ship's burn, and it never appears for a pilot holding
the key on an empty tank — the server decides who is actually running hot.

Cosmetics are purely visual and apply to **your own ship only**; loadouts aren't on the
wire, so other players see a stock hull. Progress lives in
`%LOCALAPPDATA%\AsteroidSalvage\profile.json`, tracked client-side from game events —
unlocks are personal and cosmetic, so this needs no protocol changes and can't affect
anyone else's match.

The tractor beam is short range — **15 m** (`grab.range`) — so you fly up to a rock and
take it rather than vacuuming the field from a standoff. It locks onto whatever is
closest to your crosshair, selecting by *aim angle* rather than proximity, so a nearer
rock off to the side can't steal the lock from the one you lined up on.

`grab.range` and `grab.hold_distance` interact: cargo is held at
`hold_distance + both radii`, which is already ~13.5 m for the biggest asteroids. Raising
one usually means raising the other.

The **green arrow** floating above your ship always points at the mothership. Grab a rock,
follow the arrow home, and it banks automatically once you or your cargo is inside the
60 m capture radius — the HUD counts you down and switches to `DELIVERING...`.

### Asteroid tiers

| Tier | Colour | Per rock | Per kg | Notes |
|---|---|---|---|---|
| rubble | grey | 16 | 8.2 | clutter |
| iron | rust | 211 | 7.9 | bread and butter |
| crystal | cyan | 325 | **135** | light and lucrative — the efficiency play |
| gold | yellow | 879 | 19 | dense for its size |
| massive | dark red | **2891** | 4.7 | **needs two beams** |

A massive asteroid will not move for a lone stock beam *at all* — bring a teammate, or
buy the Tractor Amplifier. Payout splits between everyone with a beam on it, so helping
is never charity.

### Bolts travel, and they fall

Lasers are no longer hitscan. Rounds leave the barrel at **420 m/s** and are pulled down
world -Z at **22 m/s²**, which changes what aiming is: you lead a moving target, and at
distance you hold high. A 300 m shot is in the air 0.7 s and falls about 5.5 m.

The drop is not physics — there is no gravity out here. It is a deliberate arc so that a
long shot is a judgement rather than a straight line, and so the reticle has something to
be wrong about. Station turrets fire the same rounds and solve their own lead and
elevation, which means a fast crosser can now beat a station's guns.

Bolts are not physics bodies: they are swept segments tested against the same objects the
old raycast used, and they reach clients as synthetic snapshot entries. Sending them
rather than letting clients integrate their own arc is what keeps a near-miss from being
drawn as a hit.

`0x87 Shots` is now only a muzzle flash — shooter and team, five bytes. It used to carry
length, a hit flag and a target; none of that can be known at the trigger any more, and
leaving the fields carrying zeros immediately misled a test into asserting that nothing
ever hit anything.

### Combat and the siege

Everything that can be shot is measured against a **30-point hull** and a **4-damage**
stock laser: eight hits to kill, which is more than the six-shot magazine holds, so a kill
costs a reload and disengaging is always an option. Rocks are exempt from that rescaling —
`toughnessScale` in `tiers.go` keeps every asteroid at the time-to-break it was tuned for,
so moving the guns cannot silently retune the whole belt.

A **mothership has 500 health and can be destroyed**, which is the other way to win a
round — and the fastest one. At 4 damage a shot that is 125 hits: about a hundred seconds
for a single stock pilot, and a handful of seconds for a four-player crew with maxed
lasers. **Breaching a station knocks that crew out of the round** — and only that crew. Their
ships go with the base, they cannot respawn, and everyone else carries on; the round ends
when one crew is left, exactly as a wipe-out does. It used to end the round outright,
which with four teams on a map meant one siege ended two other crews' round for reasons
that had nothing to do with them. Stations are rebuilt between rounds, so a successful
siege does not hand you every round after it.

**Stations cannot be damaged at all in King of the Hill.** That round is decided by
holding ground and nothing else — a breakable base would be a second, unrelated way to end
it. For the same reason, respawns are free in that mode and being wiped out does not end a
round: points are the only win condition.

At this health a station is genuinely vulnerable: leaving home undefended while the whole
crew works the belt is a real risk, and the two station upgrades are the answer rather
than a luxury.

Your own station is never a target — a stray shot lining up a delivery cannot chip the
thing you are delivering to — but an enemy station is solid, and will stop a beam that
would otherwise have crossed the map.

The two station upgrades are the answer to being sieged. Shields show up in the world as a
team-coloured wireframe bubble, so you can see what you are flying into; turrets announce
themselves by shooting. Turrets reach 220 m, which is outside the deposit radius but well
short of the belt, so they punish someone who came for the station and never plink at
somebody working.

## Game modes

Chosen on the host screen, which swaps in that mode's own settings when you pick it.
Everything else — flight, the map, combat, lives, the match structure — is shared, so a
mode feels like the same game rather than a different one.

| Mode | How you score | Its own settings |
|---|---|---|
| **Salvage** | Tractor rocks home and bank them at your station | — |
| **Hoard** | No hauling: **fly into rocks to eat them** | points to win a round, rock value |
| **King of the Hill** | **Hold a marked zone**, a point a second | points to win, points per second, credits per second, how often the hill moves |

**Hoard** is the scramble. No trip home and no cargo to protect, so the round is a race to
be where the good rocks are. Rock integrity still counts, so one somebody has been
bouncing off the scenery is worth less. `-mode hoard -hoard-target 4000 -hoard-bonus 1.6`

**King of the Hill** is the territory game: a 130 m zone somewhere on the map, and a crew
scores for every second they are the only crew inside it. Three rules carry the mode —
presence rather than kills, so a crew losing the shooting can still win by being
somewhere; **contested means nobody scores**, because if the leader still scored while
contested the correct play against them would be to leave; and the hill relocates on a
timer, since a fixed one becomes an entrenched position and a queue of people flying into
it. Holding also pays **20 credits a second to every member of the crew** — not only the ship
parked in the volume, since escorting and chasing people off it is the other half of the
job. Round points win the round and reset with it; credits are personal and last the
match, so a crew that has already lost a round on the clock still has a reason to contest
the hill. `-mode koth -koth-target 500 -koth-rate 7 -koth-credits 20 -koth-shift 45`

The zone is a volume you fly *through* — giving it a collider would make holding it a
matter of bouncing off it — so it exists only as a synthetic body in the snapshot, drawn
as a wireframe shell in the holder's colour and neutral white while empty or contested.

### Nebula and dust

Speed is only legible as parallax, and parallax needs something close. The starfield is a
shell parented to the camera so it never shifts, and asteroids are hundreds of metres out —
at 40 m/s the screen was almost completely still.

Two layers fix it: **dust**, a few hundred motes in a small box that follows the ship and
snaps to a lattice of its own size, so the motes hold still in *world* space and stream
past the canopy instead of travelling with you; and **nebula**, seven enormous additive
clouds ringing the play volume that slide against the starfield as you cross the map.

### The map

**A new map every match.** The seed defaults to a fresh one each run and is logged, so a
map worth keeping can be replayed with `-seed`.

Every map is built around **one enormous centrepiece** — 260–400 m, roughly ten times a
mothership — and which of the four structures it is changes from match to match. That
single choice is what makes a map a place rather than a scattering: the belt is worked
around it, and it is visible from every station on the ring. Nine smaller structures are
scattered outside the mothership ring.

The four structures: **battlestations** (a cracked armoured
sphere with a dish, cheerfully derivative), **capital wrecks** snapped in half, **planet
chunks** with the core still glowing through the crust, and **ruined ring stations**. Nine
laid out by rejection sampling so nothing overlaps a hangar, another structure, or the
centrepiece — and asteroids will not generate inside the landmark, where they would be
unreachable.

They are static, indestructible and **solid — a laser stops at one**. That is the point:
they are cover to break line of sight behind and landmarks to navigate by, in a volume
that was otherwise uniform in every direction. Generation is fully deterministic in the
seed, so a host can share a map and a generation bug can be reproduced rather than hunted.

### Ramming

Flying your hull into something at speed is a weapon, above a 10 m/s closing speed so a
nudge while you both reach for the same rock is still just a nudge.

| You hit | Result |
|---|---|
| An enemy ship | **both** ships are destroyed |
| An enemy station | **15 damage** to it, and your ship is gone |
| A teammate | nothing — you bounce |

It always costs you the ship, which is what stops it replacing the laser. A station has
500 health, so ramming one down would take 34 ships and nobody has that many; what it buys
a crew already shooting a station is a few seconds off the clock.

### Lives

Each team shares a pool of respawns per round — **6 to 20, twelve by default**, set on the
host screen or with `-lives`. Every death spends one, whoever it was and however it
happened, so the pilot who keeps ramming is spending everybody's budget.

Spend the last one and you are **out for the round** — no countdown, because none is
coming. When only one crew still has anyone able to fly, the round ends and goes to them,
exactly like a station breach. Lives reset every round, so a round lost on lives costs you
the round rather than the match. Everyone can see everyone's remaining count on the
scoreboard: a rival down to their last ship is information worth acting on.

### Boost is a tank with a stall

Holding boost drains a four-second tank that refills over seven. Running it **all the way
down stalls it for three seconds** — nothing refills, and no burn can start — and starting
a burn afterwards needs a real second of charge in it rather than a drop.

Both rules exist for the same reason. With only a lockout-until-release, a pilot who
tapped the key kept re-engaging on whatever had trickled in since the last tick, so boost
was never actually unavailable; it just stuttered. The HUD draws four states, because
pressing the key does something different in each: burning, ready, charging-but-not-yet-
usable, and stalled with a countdown.

### Match structure

Best of 5 rounds, 3 minutes each, majority takes the match. Team scores reset every
round; **personal credits and upgrades persist**, which is what makes a round you lost
still worth playing. Between rounds a 45-second intermission opens the shop — press
`1`–`4` to buy:

| Upgrade | Effect | From |
|---|---|---|
| Tractor Amplifier | Beam counts as two — solo a massive | 1200 cr |
| Engine Boost | +25% thrust per level (×3) | 400 cr |
| Beam Extender | +8 m reach per level (×3) | 350 cr |
| Cargo Dampeners | Cargo takes 35% less damage per level (×2) | 500 cr |
| Laser Focuser | +40% damage per level (×3) | 450 cr |
| Capacitor Bank | +2 shots per charge (×3) | 400 cr |
| Fast Recharger | Charge refills 20% quicker per level (×3) | 380 cr |
| **Station Shields** | **TEAM**: your mothership takes 20% less per level (×3) | 900 cr |
| **Station Turrets** | **TEAM**: your mothership shoots back, one gun per level (×3) | 1000 cr |

The last two are the only things in the shop you buy for the crew rather than for
yourself. They count up on the *team*, so a shield a crewmate paid for in round two is
still there for someone who joins in round three, and two people buying it does not get
you two shields.

Server flags for pacing: `-bestof 5 -round 180 -intermission 45 -warmup 15 -lives 12`, or
`-sandbox` for one endless round with no clock or shop.

**HOST A GAME owns the server.** It launches one with the settings on that screen, so
those settings only apply to a server it started. If something is already listening on the
port it now says so and refuses, rather than quietly connecting to the other server and
playing its match — which is what made "round length does nothing, it is always three
minutes" happen: `play.bat` used to pre-start a server, and the menu's copy could never
bind.

Client flags: `--name`, `--team`, `--sensitivity`, `--invert-y`.

## Testing

```bash
cd server && go test ./...                      # physics, sim, netcode
go run ./bots -n 16 -seconds 60                 # 16-player load test
python client/smoke_test.py                     # Python client vs the real Go server
python client/playtest.py                       # full loop: grab a rock, deliver it
python client/soaktest.py --seconds 300         # connection stability over time
python tools/devconsole/main.py --frames 120    # console renders without error

# See what the game actually looks like, without a human at the keyboard:
python client/main.py --demo --shot out.png --shot-frames 300 --shot-hauling
python client/preview.py --model mothership --out bin/ms.png   # a model on its own
```

`--demo` autopilots using the same body-relative steering the mouse produces, and
`--shot-hauling` waits until cargo is on the beam before capturing — so a screenshot
shows the mechanic rather than a ship parked at spawn. Capturing the desktop from
outside is unreliable; it photographs whichever window is on top.

The smoke test matters more than it looks: Go and Python implement the wire protocol
independently, and drift between them is the most likely bug in this project.
`TestPythonEncodersDecodeInGo` pins the other direction with literal bytes.

## Tuning the feel

Grab feel cannot be unit-tested. Run the server with `-dev`, open the console, fly, and
drag sliders while playing — changes apply on the next tick with no restart. The server
writes `config/feel.toml` on Ctrl-C so a good session is kept.

The parameter that matters most is **`grab.reaction_scale`**: how much of the cargo's
reaction force your ship feels. Physically it should be `1.0`. Whether that is what
*feels* right is the entire question.

Ships run at **`ship.thrust` 1800**, which is twice what they used to: about 43 m/s
unladen, 86 m/s on boost. Doubling thrust rather than halving damping was deliberate —
it lifts top speed *and* acceleration while leaving the response time unchanged, where
cutting drag would have doubled the top speed and made the ship floaty to start and slow
to stop.

Two thresholds are expressed in m/s and had to move with it, because what matters is the
fraction of top speed rather than the absolute number: `salvage.damage_threshold` 6 → 12,
so ordinary cruising does not shred cargo, and `RamSpeedThreshold` 10 → 20, so an
incidental bump between rivals is not a double kill.

Current balance: the heaviest asteroid leaves ~49% of unladen mobility — heavy, but still
flyable. `TestCarryingHeaviestRockSlowsButDoesNotStopTheShip` guards that. It was ~34%
before the engines were doubled: more thrust makes the biggest rocks noticeably lighter
work, which is the one balance consequence of the speed change worth watching.

## Notes on the vendored libraries

Three upstream issues were worked around rather than patched, since these are submodules:

- **`lagrange/math_simd.h` does not compile on plain x86-64.** It only includes the Intel
  intrinsics headers under `__AVX2__` but selects the `__m128` path under `__SSE__`
  (always defined on x86-64), and calls `_mm_rsqrt_ss` with two arguments when it takes
  one. `server/physics/bridge.c` includes `lagrange/sim.h` directly instead of the
  umbrella header, which avoids that file entirely. Orbital mechanics and libspatial
  broadphase would need it fixed.
- **`lagrange`'s broadphase is O(n²)** (`lg_broad_phase`, sim.h:711) and its entity lookup
  is a linear scan. Measured anyway: 1000 bodies costs 1.97 ms against a 33 ms budget, so
  libspatial broadphase is not needed until roughly 2000 bodies.
- **`sprocket`'s `Room.BroadcastBinary` does not deliver.** It enqueues via
  `writeSharedNoSignal` and then wakes workers with nil sentinels, which `writeWorker`
  treats as no-ops; nothing ever flushes those queues. `server/net/server.go` broadcasts
  by iterating `Room.Members()` and calling `Session.WriteBinary`, which works and also
  copies the payload (letting the snapshot buffer be reused).
- **`sprocket` drops every connection after ~60s.** Its gws transport has empty `OnPing`
  and `OnPong` stubs (`transports/gws/gws.go:73,75`), so the server never answers a
  client's ping *and* never records a pong. A session's read deadline is only extended
  by the pong handler (`session.go:241-246`), so it expires at `PongWait` no matter how
  much traffic flows, and `pingWorker` separately reaps the session once `lastPong` ages
  out. The server sets `PongWait` beyond any real session to disable both, and
  `runKeepalive` refreshes read deadlines from actually-received messages instead —
  better liveness for a game where clients send input 30×/s. The client also passes
  `ping_interval=None`, since its pings would otherwise go unanswered.
- **`lagrange`'s contact normals are inverted relative to its resolver.** The narrow
  phase builds the normal A→B while `lg_resolve_contact` assumes B→A, so approaching
  bodies are treated as separating and ignored, then attracted once they overlap. Every
  collider is flagged as a trigger to disable lagrange's pass, and `bridge.c` resolves
  sphere contacts with a consistent convention.

## Layout

```
vendor/lagrange/      C physics submodule (contains libspatial)
server/
  physics/            cgo bridge — bulk API over lagrange's per-entity linear scans
  sim/                game rules: ship control, grab, fragility, scoring
  net/                wire protocol + sprocket server + Go client
  bots/               headless load-test clients
client/               Panda3D client
tools/devconsole/     ImGui tuning console
shared/               protocol.md (authoritative) + its Python implementation
config/feel.toml      tuned values
```

## Status

Playable end to end: fly, grab, haul, bank, shoot, break rocks open for their cores,
siege a rival's station, waves escalate, teams score, co-op works. 16 concurrent bots
sustain 15 Hz with zero errors.

Not built yet: lobby and ready-up, an end-of-match results screen (the losing stations
already blow up; there is no scoreboard after it), client-side prediction, and interest
management (deferred until profiling demands it).
