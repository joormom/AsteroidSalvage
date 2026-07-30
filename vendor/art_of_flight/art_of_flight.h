#ifndef ART_OF_FLIGHT_H
#define ART_OF_FLIGHT_H

#include <math.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @file art_of_flight.h
 * @brief ART_OF_FLIGHT v0.1.0 — zero-dep C99 fly-by-wire for games & sims
 * @author Damus <damus@straylightrun.org>
 * @version 0.1.0
 *
 * A single-header, zero-allocation flight control library for physics-driven
 * spacecraft and aircraft.  Provides attitude/rate control, 6-DOF
 * translation, and input shaping — all in ~800 lines of C99.
 *
 * Compile: -std=c99 -O3 -ffast-math -pedantic -Wall -Wextra
 * All functions are static inline.  Every non-aliasing pointer carries
 * restrict.  Hot structs are padded to 16 bytes for future SIMD/NEON.
 *
 * ---------------------------------------------------------------------------
 * QUICK START
 * ---------------------------------------------------------------------------
 * 1.  Create an aof_flight_config and fill it with aof_default_config().
 * 2.  Create three aof_pid_state (pitch, yaw, roll) and init with aof_pid_init().
 * 3.  Every frame, in this order:
 *
 *     A.  DESIRED ATTITUDE
 *         aof_quat q_des = aof_look_rotation(&world_target_dir, &world_up);
 *         // or: aof_quat q_des = aof_quat_identity();  // for rate-only mode
 *
 *     B.  ATTITUDE ERROR → RATE SETPOINTS (outer loop)
 *         aof_flight_targets rates = aof_attitude_to_rates(&q_cur, &q_des, &cfg);
 *         rates = aof_limit_rates(&rates, cfg.max_rate_rad);
 *
 *     C.  RATE PIDs → TORQUE (inner loop)
 *         aof_vec3 torque;
 *         aof_pid_update3(&pid_p, &pid_y, &pid_r,
 *                         &rates, &measured_omega, dt, 1, &torque);
 *         // use_dom=1 (derivative-on-measurement) recommended for mouse aim
 *
 *     D.  TRANSLATION LQR (optional 6-DOF)
 *         aof_lqr_3d lqr;  aof_lqr_3d_init(&lqr, 10.0f, 2.0f, 1.0f);
 *         aof_vec3 pos_err = aof_sub3(&target_pos, &current_pos);
 *         aof_vec3 vel_err = aof_sub3(&target_vel, &current_vel);
 *         aof_vec3 thrust = aof_lqr_3d_update(&lqr, &pos_err, &vel_err);
 *         // If you have gravity:
 *         thrust = aof_add3(&thrust, &gravity_compensation);
 *
 *     E.  APPLY
 *         physics_apply_force(&body, thrust);
 *         physics_apply_torque(&body, torque);
 *
 * ---------------------------------------------------------------------------
 * COORDINATE FRAMES
 * ---------------------------------------------------------------------------
 *   World frame:  right-handed, Y-up, Z-forward (same as Unity/Unreal default).
 *   Body frame:   right-handed, Y-up, Z-forward (aligned with ship mesh).
 *   Quaternions:  (w, x, y, z), Hamilton product order, unit norm.
 *
 *   ALL direction vectors passed to public functions MUST be unit length
 *   unless the function explicitly normalises them (e.g. aof_look_rotation).
 *
 * ---------------------------------------------------------------------------
 * CONTROL ARCHITECTURE
 * ---------------------------------------------------------------------------
 *   Two-loop cascade:
 *     Outer:  position/attitude  →  generates velocity/rate setpoints
 *     Inner:  velocity/rate PIDs  →  generates force/torque commands
 *
 *   For pure mouse-aim: skip the outer quaternion loop and
 *   use aof_process_rate_input() to map screen-space cursor directly to
 *   body-rate commands, plus optional auto-bank.
 *
 * ---------------------------------------------------------------------------
 * RETURN CODES
 * ---------------------------------------------------------------------------
 *   aof_look_rotation() :  0 = success, -1 = forward near-zero, -2 = forward ∥ up
 *   aof_normalize3()    :  0 = success, -1 = near-zero vector (undefined output)
 *   aof_quat_normalize():  0 = success, -1 = near-zero quaternion (undefined output)
 */

/* ==========================================================================
 * Types
 * ========================================================================== */

typedef struct {
    float x, y, z;
    float _pad;                     /* 16-byte for AVX / NEON friendliness */
} aof_vec3;

typedef struct {
    float w, x, y, z;               /* unit quaternion (w, x, y, z) */
} aof_quat;

typedef struct {
    float pitch_sensitivity;        /* body-rate scale, typical 2.0–3.0 */
    float yaw_sensitivity;
    float roll_sensitivity;
    float bank_limit_rad;           /* max auto-bank, e.g. 0.785f (45°) */
    float bank_gain;                /* P-gain bank error → roll cmd */
    float deadzone;                 /* [0,1), typical 0.02–0.08 */
    float exponent;                 /* shaping power ≥ 1.0, 1.5–2.5 feels good */
    float max_rate_rad;             /* hard clamp on rate commands [rad/s] */
} aof_flight_config;

typedef struct {
    float pitch;                    /* normalised rate cmd [-1,1] or [rad/s] */
    float yaw;
    float roll;
} aof_flight_targets;

typedef struct {
    aof_vec3 thrust;                /* translation (acceleration or force) */
    aof_flight_targets rates;       /* rotation commands */
} aof_6dof_cmd;

typedef struct {
    float kp, ki, kd;
    float integral;
    float prev_error;
    float prev_measured;            /* for derivative-on-measurement */
    float integral_limit;           /* |integral| clamp – set at init */
} aof_pid_state;

typedef struct {
    float k_pos;
    float k_vel;
} aof_lqr_1d;

typedef struct {
    aof_lqr_1d x, y, z;
} aof_lqr_3d;

/* ==========================================================================
 * Scalar helpers
 * ========================================================================== */

/** Branchless clamp.  If lo > hi, result is hi (fminf wins). */
static inline float aof_clampf(float v, float lo, float hi)
{
    return fminf(fmaxf(v, lo), hi);
}

/* ==========================================================================
 * Vector helpers
 * ========================================================================== */

static inline float aof_len3(const aof_vec3 *restrict v)
{
    return sqrtf(v->x * v->x + v->y * v->y + v->z * v->z);
}

/**
 * Normalise a vector in place.
 * @return 0 on success, -1 if near-zero (output is left undefined).
 */
static inline int aof_normalize3(aof_vec3 *restrict v)
{
    float n = aof_len3(v);
    if (n < 1.0e-12f)
        return -1;
    float inv = 1.0f / n;
    v->x *= inv; v->y *= inv; v->z *= inv;
    return 0;
}

static inline float aof_dot3(const aof_vec3 *restrict a, const aof_vec3 *restrict b)
{
    return a->x * b->x + a->y * b->y + a->z * b->z;
}

static inline aof_vec3 aof_cross3(const aof_vec3 *restrict a, const aof_vec3 *restrict b)
{
    aof_vec3 r = {
        a->y * b->z - a->z * b->y,
        a->z * b->x - a->x * b->z,
        a->x * b->y - a->y * b->x,
        0.0f
    };
    return r;
}

static inline aof_vec3 aof_scale3(const aof_vec3 *restrict v, float s)
{
    aof_vec3 r = { v->x * s, v->y * s, v->z * s, 0.0f };
    return r;
}

static inline aof_vec3 aof_add3(const aof_vec3 *restrict a, const aof_vec3 *restrict b)
{
    aof_vec3 r = { a->x + b->x, a->y + b->y, a->z + b->z, 0.0f };
    return r;
}

static inline aof_vec3 aof_sub3(const aof_vec3 *restrict a, const aof_vec3 *restrict b)
{
    aof_vec3 r = { a->x - b->x, a->y - b->y, a->z - b->z, 0.0f };
    return r;
}

/* ==========================================================================
 * Quaternion helpers
 * ========================================================================== */

static inline aof_quat aof_quat_identity(void)
{
    aof_quat q = { 1.0f, 0.0f, 0.0f, 0.0f };
    return q;
}

static inline aof_quat aof_quat_conj(const aof_quat *restrict q)
{
    aof_quat r = { q->w, -q->x, -q->y, -q->z };
    return r;
}

/** Hamilton product: r = a ⊗ b  (standard active-rotation convention). */
static inline aof_quat aof_quat_mul(const aof_quat *restrict a, const aof_quat *restrict b)
{
    aof_quat r;
    r.w = a->w*b->w - a->x*b->x - a->y*b->y - a->z*b->z;
    r.x = a->w*b->x + a->x*b->w + a->y*b->z - a->z*b->y;
    r.y = a->w*b->y - a->x*b->z + a->y*b->w + a->z*b->x;
    r.z = a->w*b->z + a->x*b->y - a->y*b->x + a->z*b->w;
    return r;
}

/**
 * Rotate a vector by a unit quaternion.
 * Uses the optimised form v' = v + 2w(q×v) + 2q×(q×v).
 */
static inline aof_vec3 aof_quat_rotate(const aof_quat *restrict q, const aof_vec3 *restrict v)
{
    float tx = 2.0f * (q->y * v->z - q->z * v->y);
    float ty = 2.0f * (q->z * v->x - q->x * v->z);
    float tz = 2.0f * (q->x * v->y - q->y * v->x);
    aof_vec3 r = {
        v->x + q->w * tx + (q->y * tz - q->z * ty),
        v->y + q->w * ty + (q->z * tx - q->x * tz),
        v->z + q->w * tz + (q->x * ty - q->y * tx),
        0.0f
    };
    return r;
}

/**
 * Normalise a quaternion in place.
 * @return 0 on success, -1 if near-zero (output is left undefined).
 */
static inline int aof_quat_normalize(aof_quat *restrict q)
{
    float n2 = q->w*q->w + q->x*q->x + q->y*q->y + q->z*q->z;
    if (n2 < 1.0e-12f)
        return -1;
    float inv = 1.0f / sqrtf(n2);
    q->w *= inv; q->x *= inv; q->y *= inv; q->z *= inv;
    return 0;
}

/**
 * Construct a quaternion from a unit axis and angle [rad].
 * q = (cos(θ/2), axis·sin(θ/2)).
 */
static inline aof_quat aof_quat_from_axis_angle(const aof_vec3 *restrict axis, float angle)
{
    float h = 0.5f * angle;
    float s = sinf(h), c = cosf(h);
    aof_quat q = { c, axis->x * s, axis->y * s, axis->z * s };
    return q;
}

/**
 * Spherical linear interpolation between two unit quaternions.
 * t ∈ [0,1].  Returns a unit quaternion (exact, not approximated).
 */
static inline aof_quat aof_quat_slerp(const aof_quat *restrict a,
                                       const aof_quat *restrict b, float t)
{
    float dot = a->w*b->w + a->x*b->x + a->y*b->y + a->z*b->z;
    aof_quat b2 = *b;
    if (dot < 0.0f) {
        dot = -dot;
        b2.w = -b2.w; b2.x = -b2.x; b2.y = -b2.y; b2.z = -b2.z;
    }
    if (dot > 0.9995f) {
        /* Near-parallel: lerp to avoid division by tiny sin(theta) */
        aof_quat r = {
            a->w + t*(b2.w - a->w),
            a->x + t*(b2.x - a->x),
            a->y + t*(b2.y - a->y),
            a->z + t*(b2.z - a->z)
        };
        aof_quat_normalize(&r);
        return r;
    }
    float theta0 = acosf(aof_clampf(dot, -1.0f, 1.0f));
    float theta  = theta0 * t;
    float s0 = cosf(theta) - dot * sinf(theta) / sinf(theta0);
    float s1 = sinf(theta) / sinf(theta0);
    aof_quat r = {
        a->w * s0 + b2.w * s1,
        a->x * s0 + b2.x * s1,
        a->y * s0 + b2.y * s1,
        a->z * s0 + b2.z * s1
    };
    return r;
}

/**
 * Integrate a constant body-frame angular velocity over dt using the
 * quaternion exponential map (exact for constant ω).
 *
 * q_new = q ⊗ exp(½·ω·dt)
 *       = q ⊗ (cos(|v|/2), v̂·sin(|v|/2))   where v = ω·dt
 *
 * This is the Lie-group equivalent of Euler integration for rotations.
 * It preserves unit norm exactly (to within floating-point error) and
 * avoids the phase-lag of naive reprojection methods.
 */
static inline aof_quat aof_quat_integrate(const aof_quat *restrict q,
                                           const aof_vec3 *restrict omega,
                                           float dt)
{
    aof_vec3 v = { 0.5f * omega->x * dt,
                   0.5f * omega->y * dt,
                   0.5f * omega->z * dt, 0.0f };
    float n2 = v.x*v.x + v.y*v.y + v.z*v.z;
    float n  = sqrtf(n2);

    float c, s;
    if (n < 1.0e-8f) {
        /* Taylor series: cos(x)≈1, sin(x)/x≈1 for tiny angles */
        c = 1.0f - 0.5f * n2;   /* 1 - x²/2 */
        s = 1.0f - n2 / 6.0f;   /* 1 - x²/6 */
    } else {
        c = cosf(n);
        s = sinf(n) / n;
    }

    aof_quat delta = { c, v.x * s, v.y * s, v.z * s };
    return aof_quat_mul(q, &delta);
}

/**
 * Attitude error quaternion: q_err = q_des ⊗ q_cur*
 *
 * The vector part of q_err is approximately ½·rotation_vector for small
 * angles.  We ensure the shortest-path branch (w ≥ 0) before returning.
 */
static inline aof_vec3 aof_quat_error_vec(const aof_quat *restrict q_des,
                                          const aof_quat *restrict q_cur)
{
    aof_quat qc = aof_quat_conj(q_cur);
    aof_quat qe = aof_quat_mul(q_des, &qc);
    if (qe.w < 0.0f) {
        qe.w = -qe.w; qe.x = -qe.x; qe.y = -qe.y; qe.z = -qe.z;
    }
    aof_vec3 e = { qe.x, qe.y, qe.z, 0.0f };
    return e;
}

/* ==========================================================================
 * Input shaping
 * ========================================================================== */

/**
 * Deadzone + power curve for joystick / mouse input.
 *
 * raw       : [-1, 1]  (e.g. normalised mouse X or stick axis)
 * deadzone  : [0, 1)   inputs below this return 0 (stick drift compensation)
 * exponent  : ≥ 1.0    1.0 = linear, 2.0 = quadratic, 1.8 = typical mouse feel
 *
 * Returns shaped value in [-1, 1].
 *
 * Example:
 *   float pitch_cmd = aof_shape_input(mouse_y, 0.04f, 1.8f);
 */
static inline float aof_shape_input(float raw, float deadzone, float exponent)
{
    if (exponent <= 0.0f)
        return copysignf(1.0f, raw);   /* degenerate: full deflection */

    float abs_in = fabsf(raw);
    if (abs_in <= deadzone)
        return 0.0f;

    float remapped = (abs_in - deadzone) / (1.0f - deadzone);
    float curved;
    if (exponent == 1.0f)      curved = remapped;
    else if (exponent == 2.0f) curved = remapped * remapped;
    else                       curved = powf(remapped, exponent);
    return copysignf(curved, raw);
}

/* ==========================================================================
 * Slew / rate limiting
 * ========================================================================== */

/**
 * Scalar slew limiter.
 * Output can only change by max_delta per call.  Use this to smooth
 * camera transitions or prevent actuator saturation snaps.
 */
static inline float aof_slew_rate(float current, float target, float max_delta)
{
    float d = target - current;
    if (d >  max_delta) return current + max_delta;
    if (d < -max_delta) return current - max_delta;
    return target;
}

/** Per-axis slew limiter. */
static inline aof_vec3 aof_slew3(const aof_vec3 *restrict cur,
                                 const aof_vec3 *restrict tgt, float max_delta)
{
    aof_vec3 r = {
        aof_slew_rate(cur->x, tgt->x, max_delta),
        aof_slew_rate(cur->y, tgt->y, max_delta),
        aof_slew_rate(cur->z, tgt->z, max_delta),
        0.0f
    };
    return r;
}

/**
 * Clamp physical rate commands to [-max_rate_rad, +max_rate_rad].
 * If max_rate_rad ≤ 0, returns the input unchanged (useful for
 * normalised [-1,1] mode where you clamp elsewhere).
 */
static inline aof_flight_targets aof_limit_rates(const aof_flight_targets *restrict rates,
                                                  float max_rate_rad)
{
    aof_flight_targets out = *rates;
    if (max_rate_rad > 0.0f) {
        out.pitch = aof_clampf(out.pitch, -max_rate_rad, max_rate_rad);
        out.yaw   = aof_clampf(out.yaw,   -max_rate_rad, max_rate_rad);
        out.roll  = aof_clampf(out.roll,  -max_rate_rad, max_rate_rad);
    }
    return out;
}

/* ==========================================================================
 * Independent PIDs
 * ========================================================================== */

/**
 * Initialise a single-axis PID.
 *
 * kp, ki, kd         : classic gains (all positive)
 * integral_limit     : anti-windup clamp.  Rule of thumb:
 *                      max_actuator_output / ki, or 10.0f if unsure.
 */
static inline void aof_pid_init(aof_pid_state *restrict pid,
                                float kp, float ki, float kd, float integral_limit)
{
    pid->kp = kp; pid->ki = ki; pid->kd = kd;
    pid->integral = 0.0f;
    pid->prev_error = 0.0f;
    pid->prev_measured = 0.0f;
    pid->integral_limit = (integral_limit > 0.0f) ? integral_limit : 1.0f;
}

/** Zero integral & history without touching gains.  Call on respawn, pause,
 *  or when switching flight modes (e.g. docking → combat). */
static inline void aof_pid_reset(aof_pid_state *restrict pid)
{
    pid->integral = 0.0f;
    pid->prev_error = 0.0f;
    pid->prev_measured = 0.0f;
}

/**
 * Standard parallel-form PID (derivative on error).
 *
 * Use this when the setpoint is smooth (e.g. autopilot trajectory,
 * AI waypoint following).  For discontinuous setpoints (mouse aim,
 * key taps) use aof_pid_update_dom() instead to avoid derivative kick.
 *
 * dt is clamped to (0, 0.1s] to protect against hitches, breakpoints,
 * or alt-tab stutter.
 */
static inline float aof_pid_update(aof_pid_state *restrict pid,
                                   float setpoint, float measured, float dt)
{
    if (dt <= 0.0f) return 0.0f;
    if (dt > 0.1f)  dt = 0.1f;

    float error = setpoint - measured;
    float p = pid->kp * error;

    pid->integral += error * dt;
    pid->integral = aof_clampf(pid->integral, -pid->integral_limit, pid->integral_limit);
    float i = pid->ki * pid->integral;

    float d = pid->kd * (error - pid->prev_error) / dt;
    pid->prev_error = error;

    return p + i + d;
}

/**
 * Derivative-on-Measurement PID.
 *
 * d = -kd · (measured - prev_measured) / dt
 *
 * This treats the derivative term as pure system damping: it responds to
 * how fast the BODY is moving, not how fast the TARGET is jumping.
 * Essential for mouse-aim where the setpoint snaps discontinuously
 * every frame.
 */
static inline float aof_pid_update_dom(aof_pid_state *restrict pid,
                                        float setpoint, float measured, float dt)
{
    if (dt <= 0.0f) return 0.0f;
    if (dt > 0.1f)  dt = 0.1f;

    float error = setpoint - measured;
    float p = pid->kp * error;

    pid->integral += error * dt;
    pid->integral = aof_clampf(pid->integral, -pid->integral_limit, pid->integral_limit);
    float i = pid->ki * pid->integral;

    float d = -pid->kd * (measured - pid->prev_measured) / dt;
    pid->prev_measured = measured;

    return p + i + d;
}

/**
 * Three-axis PID update in one call.
 *
 * use_dom != 0  →  derivative-on-measurement (recommended for mouse aim).
 * use_dom == 0  →  derivative-on-error (recommended for smooth trajectories).
 *
 * measured->x = pitch rate, ->y = yaw rate, ->z = roll rate.
 */
static inline void aof_pid_update3(aof_pid_state *restrict pid_p,
                                   aof_pid_state *restrict pid_y,
                                   aof_pid_state *restrict pid_r,
                                   const aof_flight_targets *restrict targets,
                                   const aof_vec3 *restrict measured,
                                   float dt, int use_dom,
                                   aof_vec3 *restrict out_torque)
{
    if (use_dom) {
        out_torque->x = aof_pid_update_dom(pid_p, targets->pitch, measured->x, dt);
        out_torque->y = aof_pid_update_dom(pid_y, targets->yaw,   measured->y, dt);
        out_torque->z = aof_pid_update_dom(pid_r, targets->roll,  measured->z, dt);
    } else {
        out_torque->x = aof_pid_update(pid_p, targets->pitch, measured->x, dt);
        out_torque->y = aof_pid_update(pid_y, targets->yaw,   measured->y, dt);
        out_torque->z = aof_pid_update(pid_r, targets->roll,  measured->z, dt);
    }
    out_torque->_pad = 0.0f;
}

/* ==========================================================================
 * Auto-bank (optional atmospheric flavour)
 * ========================================================================== */

/**
 * Compute an atmospheric-style auto-roll command.
 *
 * When the ship turns, it banks into the turn like a fixed-wing aircraft.
 * The bank angle is proportional to mouse_x (turn intent) and throttle
 * (faster flight = steeper bank).
 *
 * Protected against zenith/nadir gimbal singularities: if the ship is
 * pointing straight up or down relative to ref_up, cross product vanishes
 * and the function returns 0 (no roll command) rather than snapping.
 */
static inline float aof_compute_auto_roll(const aof_vec3 *restrict ship_up,
                                          const aof_vec3 *restrict ship_fwd,
                                          const aof_vec3 *restrict ref_up,
                                          float mouse_x_norm, float throttle,
                                          const aof_flight_config *restrict cfg)
{
    aof_vec3 cross = aof_cross3(ship_up, ref_up);
    float cross_len2 = aof_dot3(&cross, &cross);

    /* Singularity guard: ship ∥ ref_up → disable auto-bank */
    if (cross_len2 < 1.0e-6f)
        return 0.0f;

    float bank_target = aof_clampf(mouse_x_norm, -1.0f, 1.0f)
                      * aof_clampf(throttle, 0.0f, 1.0f)
                      * cfg->bank_limit_rad;

    float sin_a = aof_dot3(&cross, ship_fwd);
    float cos_a = aof_dot3(ship_up, ref_up);
    float current = atan2f(sin_a, cos_a);
    float error = current - bank_target;
    float cmd = aof_clampf(error * cfg->bank_gain, -1.0f, 1.0f);

    return aof_clampf(cmd * cfg->roll_sensitivity, -1.0f, 1.0f);
}

/* ==========================================================================
 * Core rate command from local aim direction
 * ========================================================================== */

/**
 * Map a body-local target direction (e.g. from a screen-space raycast into
 * ship local space) to pitch/yaw/roll rate commands.
 *
 * local_target_dir : unit vector in body frame pointing at cursor
 * ship_up/fwd      : unit world-frame orientation vectors (for auto-bank)
 * ref_up           : world up (typically (0,1,0))
 * mouse_x_norm     : [-1,1] screen-space X (used for bank intensity)
 * throttle         : [0,1] engine power (used for bank intensity)
 * enable_auto_bank : 0 = pure 3-DOF mouse aim (no roll), 1 = atmospheric bank
 *
 * Returns normalised rate commands in [-1,1].  Scale by max_rate_rad or
 * feed directly into the rate PIDs depending on your physics units.
 */
static inline aof_flight_targets aof_process_rate_input(
    const aof_vec3 *restrict local_target_dir,
    const aof_vec3 *restrict ship_up,
    const aof_vec3 *restrict ship_fwd,
    const aof_vec3 *restrict ref_up,
    float mouse_x_norm, float throttle,
    const aof_flight_config *restrict cfg, int enable_auto_bank)
{
    aof_flight_targets out = {0};
    float sx = aof_shape_input( local_target_dir->x, cfg->deadzone, cfg->exponent);
    float sy = aof_shape_input(-local_target_dir->y, cfg->deadzone, cfg->exponent);
    out.pitch = aof_clampf(sy * cfg->pitch_sensitivity, -1.0f, 1.0f);
    out.yaw   = aof_clampf(sx * cfg->yaw_sensitivity,   -1.0f, 1.0f);
    out.roll  = enable_auto_bank
              ? aof_compute_auto_roll(ship_up, ship_fwd, ref_up, mouse_x_norm, throttle, cfg)
              : 0.0f;
    return out;
}

/* ==========================================================================
 * Quaternion attitude control
 * ========================================================================== */

/**
 * Convert attitude error to body-rate commands.
 *
 * This is the OUTER loop of a two-loop cascade:
 *   q_cur → error → rates → (inner PID) → torque
 *
 * q_cur : current orientation (world → body)
 * q_des : desired orientation (from aof_look_rotation, waypoint, etc.)
 *
 * Returns rate commands.  Clamp with aof_limit_rates() before feeding to PIDs.
 */
static inline aof_flight_targets aof_attitude_to_rates(
    const aof_quat *restrict q_cur, const aof_quat *restrict q_des,
    const aof_flight_config *restrict cfg)
{
    aof_vec3 err = aof_quat_error_vec(q_des, q_cur);
    aof_flight_targets out = {
        aof_clampf(err.x * cfg->pitch_sensitivity, -1.0f, 1.0f),
        aof_clampf(err.y * cfg->yaw_sensitivity,   -1.0f, 1.0f),
        aof_clampf(err.z * cfg->roll_sensitivity,  -1.0f, 1.0f)
    };
    return out;
}

/**
 * Construct a desired orientation that points +Z (body forward) at a
 * world-space direction while keeping a preferred up vector (minimal twist).
 *
 * This is the standard "look at" rotation used by cameras and turrets.
 * The forward vector is normalised internally; up must be unit length.
 *
 * @param forward  world-space target direction (will be normalised)
 * @param up       world-space preferred up vector (must be unit length)
 * @param out      receives the resulting quaternion
 * @return 0 on success, -1 if forward is near-zero, -2 if forward ∥ up.
 *         On error, out is set to identity (safe fallback).
 */
static inline int aof_look_rotation(const aof_vec3 *restrict forward,
                                    const aof_vec3 *restrict up,
                                    aof_quat *restrict out)
{
    *out = aof_quat_identity();   /* safe default before any early exit */

    aof_vec3 f = *forward;
    float fn2 = aof_dot3(&f, &f);
    if (fn2 < 1.0e-12f)
        return -1;
    f = aof_scale3(&f, 1.0f / sqrtf(fn2));

    aof_vec3 r = aof_cross3(up, &f);
    float rn = sqrtf(aof_dot3(&r, &r));
    if (rn < 1.0e-8f)
        return -2;
    r = aof_scale3(&r, 1.0f / rn);
    aof_vec3 u = aof_cross3(&f, &r);

    /* Shepperd's stable matrix→quaternion method */
    float t = r.x + u.y + f.z;
    if (t > 0.0f) {
        float s = sqrtf(t + 1.0f) * 2.0f;
        out->w = 0.25f * s;
        out->x = (u.z - f.y) / s;
        out->y = (f.x - r.z) / s;
        out->z = (r.y - u.x) / s;
    } else if (r.x > u.y && r.x > f.z) {
        float s = sqrtf(1.0f + r.x - u.y - f.z) * 2.0f;
        out->w = (u.z - f.y) / s;
        out->x = 0.25f * s;
        out->y = (u.x + r.y) / s;
        out->z = (f.x + r.z) / s;
    } else if (u.y > f.z) {
        float s = sqrtf(1.0f + u.y - r.x - f.z) * 2.0f;
        out->w = (f.x - r.z) / s;
        out->x = (u.x + r.y) / s;
        out->y = 0.25f * s;
        out->z = (f.y + u.z) / s;
    } else {
        float s = sqrtf(1.0f + f.z - r.x - u.y) * 2.0f;
        out->w = (r.y - u.x) / s;
        out->x = (f.x + r.z) / s;
        out->y = (f.y + u.z) / s;
        out->z = 0.25f * s;
    }
    aof_quat_normalize(out);
    return 0;
}

/* ==========================================================================
 * Continuous-time LQR (double integrator) — 6-DOF translation
 * ========================================================================== */

/**
 * Initialise 1-D LQR gains from cost weights.
 *
 * Plant:  ẋ = v,  v̇ = u   (double integrator, no drag)
 *
 * Cost:   J = ∫ (q_pos·e_pos² + q_vel·e_vel² + r·u²) dt
 *
 * Closed-form continuous ARE solution:
 *   k_pos = √(q_pos / r)
 *   k_vel = √(q_vel/r + 2·√(q_pos/r))
 *
 * Tuning advice:
 *   q_pos = 10.0   (aggressive position tracking)
 *   q_vel =  2.0   (moderate velocity damping)
 *   r     =  1.0   (cheap control — allow large thrust)
 *   Increase r to make the controller more gentle / fuel-efficient.
 */
static inline void aof_lqr_1d_init(aof_lqr_1d *restrict lqr,
                                    float q_pos, float q_vel, float r)
{
    float qr = q_pos / r;
    float vr = q_vel / r;
    lqr->k_pos = sqrtf(qr);
    lqr->k_vel = sqrtf(vr + 2.0f * sqrtf(qr));
}

/**
 * 1-D LQR update.
 * pos_err = pos_desired - pos_current
 * vel_err = vel_desired - vel_current
 * Returns control effort (acceleration or force, depending on your physics).
 */
static inline float aof_lqr_1d_update(const aof_lqr_1d *restrict lqr,
                                      float pos_err, float vel_err)
{
    return lqr->k_pos * pos_err + lqr->k_vel * vel_err;
}

static inline void aof_lqr_3d_init(aof_lqr_3d *restrict lqr,
                                  float q_pos, float q_vel, float r)
{
    aof_lqr_1d_init(&lqr->x, q_pos, q_vel, r);
    aof_lqr_1d_init(&lqr->y, q_pos, q_vel, r);
    aof_lqr_1d_init(&lqr->z, q_pos, q_vel, r);
}

/**
 * 3-D LQR update.  Returns a thrust/acceleration vector.
 *
 * Errors are defined as target - current (standard convention).
 * Positive errors yield positive control effort toward the target.
 */
static inline aof_vec3 aof_lqr_3d_update(const aof_lqr_3d *restrict lqr,
                                          const aof_vec3 *restrict pos_err,
                                          const aof_vec3 *restrict vel_err)
{
    aof_vec3 u = {
        aof_lqr_1d_update(&lqr->x, pos_err->x, vel_err->x),
        aof_lqr_1d_update(&lqr->y, pos_err->y, vel_err->y),
        aof_lqr_1d_update(&lqr->z, pos_err->z, vel_err->z),
        0.0f
    };
    return u;
}

/**
 * Gravity-compensated LQR update.
 *
 * When hovering in a gravity field, the LQR alone will fight a constant
 * offset (steady-state error) because the double-integrator model has no
 * gravity term.  This helper adds a feedforward gravity vector rotated
 * into the body frame so the controller can cancel it exactly.
 *
 * gravity_world : acceleration vector in world frame (e.g. {0, -9.81, 0} for Y-up)
 * q_world_to_body : current orientation (rotates world vectors into body frame)
 *
 * Example (Y-up world, body frame):
 *   aof_vec3 g = {0.0f, -9.81f, 0.0f};
 *   aof_vec3 thrust = aof_lqr_3d_update_gravity(&lqr, &pos_err, &vel_err, &g, &q);
 */
static inline aof_vec3 aof_lqr_3d_update_gravity(const aof_lqr_3d *restrict lqr,
                                                  const aof_vec3 *restrict pos_err,
                                                  const aof_vec3 *restrict vel_err,
                                                  const aof_vec3 *restrict gravity_world,
                                                  const aof_quat *restrict q_world_to_body)
{
    aof_vec3 u = aof_lqr_3d_update(lqr, pos_err, vel_err);
    aof_vec3 g_body = aof_quat_rotate(q_world_to_body, gravity_world);
    return aof_add3(&u, &g_body);
}

static inline aof_6dof_cmd aof_make_6dof_cmd(const aof_vec3 *restrict thrust,
                                              const aof_flight_targets *restrict rates)
{
    aof_6dof_cmd cmd;
    cmd.thrust = *thrust;
    cmd.rates  = *rates;
    return cmd;
}

/* ==========================================================================
 * Defaults
 * ========================================================================== */

static inline aof_flight_config aof_default_config(void)
{
    aof_flight_config c = {
        2.5f, 2.5f, 1.5f,       /* pitch, yaw, roll sensitivity */
        0.785398f, 2.0f,        /* bank limit (rad), bank gain */
        0.04f, 1.8f,            /* deadzone, exponent */
        2.5f                    /* max rate limit (rad/s) */
    };
    return c;
}

#ifdef __cplusplus
}
#endif

#endif /* ART_OF_FLIGHT_H */
