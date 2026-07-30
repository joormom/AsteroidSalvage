/*
 * bridge.h — exported C API over ART_OF_FLIGHT for cgo.
 *
 * WHY THIS FILE EXISTS
 * --------------------
 * Every function in art_of_flight.h is `static inline`, and cgo cannot call those. This
 * is the same reason physics/bridge.h exists over lagrange; see that file's header
 * comment for the longer version.
 *
 * Unlike that bridge, this one is *not* a batching boundary. There is nothing here with
 * an O(n) lookup to amortise: the state is three floats-and-gains structs per ship, so
 * one call per ship per tick is already cheap. The grouping into a single afl_update is
 * only to keep the cgo crossing count down.
 *
 * WHAT IS AND IS NOT BRIDGED
 * --------------------------
 * Only the rate-control path: input shaping, the rate clamp, and the three-axis rate
 * PID. The library's outer attitude loop (aof_attitude_to_rates, aof_look_rotation), its
 * translation LQR and its auto-bank are deliberately left alone — this game has no
 * autopilot, its thrust is direct, and it is in space, so bridging those would only add
 * surface that nothing exercises.
 *
 * AXES
 * ----
 * art_of_flight names its rate axes (pitch, yaw, roll) and this API keeps that naming.
 * Mapping them onto the game's Z-up / +Y-forward body frame is the caller's job and
 * happens in exactly one place — see flight.go.
 */

#ifndef AFL_BRIDGE_H
#define AFL_BRIDGE_H

#include <art_of_flight.h>

#ifdef __cplusplus
extern "C" {
#endif

/* One ship's rate-loop state: three independent single-axis PIDs.
 *
 * This struct is exposed rather than kept opaque behind a create/free pair the way
 * ag_world is. It is nothing but gains and integrator state — there are no internals to
 * protect and no buffers to size — so exposing it lets the Go side embed one per player
 * by value, which saves a malloc and, more usefully, a matching free that a new exit path
 * out of RemovePlayer could forget.
 *
 * All-zero memory is a valid, freshly-reset controller. */
typedef struct {
    aof_pid_state pitch, yaw, roll;
} afl_rate_ctl;

/* Gains and input shaping for one update.
 *
 * Passed in every tick rather than stored on the controller, because the dev console can
 * move any of these between two ticks (see sim.Tuning) and the whole point of that
 * console is that a slider takes effect on the very next one. */
typedef struct {
    float kp, ki, kd;
    float integral_limit;   /* |integral| clamp, anti-windup. <= 0 is treated as 1. */
    float max_rate;         /* rad/s that full input asks for; also clamps the setpoint */
    float deadzone;         /* input shaping, [0,1) */
    float exponent;         /* input shaping, >= 1 */
    int   use_dom;          /* 1 = derivative-on-measurement (correct for mouse aim) */
} afl_params;

/* Drop the integrator and the derivative history, keeping the gains (which are supplied
 * per update anyway). Call on spawn, respawn and round start: an integrator wound up
 * fighting a 600 kg asteroid, left to survive a death, would fly the new ship sideways. */
void afl_reset(afl_rate_ctl* c);

/* One tick of the rate loop.
 *
 * in_*    normalised pilot input, -1..1, as the wire protocol carries it
 * rate_*  measured *body-frame* angular rate, rad/s
 * dt      seconds; art_of_flight clamps it to (0, 0.1] internally
 * out_*   commanded body-frame torque, N*m. Must not be NULL.
 */
void afl_update(afl_rate_ctl* c, const afl_params* p,
                float in_pitch, float in_yaw, float in_roll,
                float rate_pitch, float rate_yaw, float rate_roll,
                float dt,
                float* out_pitch, float* out_yaw, float* out_roll);

#ifdef __cplusplus
}
#endif

#endif /* AFL_BRIDGE_H */
