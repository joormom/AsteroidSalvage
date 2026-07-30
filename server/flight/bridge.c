#include "bridge.h"

void afl_reset(afl_rate_ctl* c)
{
    aof_pid_reset(&c->pitch);
    aof_pid_reset(&c->yaw);
    aof_pid_reset(&c->roll);
}

/* Copy this tick's gains into the three PIDs.
 *
 * Deliberately not aof_pid_init: that also zeroes the integrator and the derivative
 * history, and calling it every tick — which is what a live dev console slider needs —
 * would silently reduce the controller to P-and-nothing-else at 30 Hz. Assigning the
 * gains and leaving the state alone is the whole difference. */
static void afl_set_gains(afl_rate_ctl* c, const afl_params* p)
{
    /* aof_pid_init guards this; assigning directly means guarding it here. A zero limit
     * would clamp the integrator to nothing. */
    float ilimit = (p->integral_limit > 0.0f) ? p->integral_limit : 1.0f;

    aof_pid_state* axes[3] = { &c->pitch, &c->yaw, &c->roll };
    for (int i = 0; i < 3; ++i) {
        axes[i]->kp = p->kp;
        axes[i]->ki = p->ki;
        axes[i]->kd = p->kd;
        axes[i]->integral_limit = ilimit;
    }
}

void afl_update(afl_rate_ctl* c, const afl_params* p,
                float in_pitch, float in_yaw, float in_roll,
                float rate_pitch, float rate_yaw, float rate_roll,
                float dt,
                float* out_pitch, float* out_yaw, float* out_roll)
{
    afl_set_gains(c, p);

    /* Shape first, scale second. aof_shape_input works on the normalised stick value, so
     * doing it in this order keeps the deadzone a fraction of stick travel rather than an
     * absolute rate that would move every time max_rate did. */
    aof_flight_targets t;
    t.pitch = aof_shape_input(in_pitch, p->deadzone, p->exponent) * p->max_rate;
    t.yaw   = aof_shape_input(in_yaw,   p->deadzone, p->exponent) * p->max_rate;
    t.roll  = aof_shape_input(in_roll,  p->deadzone, p->exponent) * p->max_rate;

    /* Redundant while input is in -1..1 — which the server clamps it to — and that is the
     * point: it is the guard that keeps a protocol change or a bot with an unclamped
     * axis from commanding an arbitrary rate. */
    t = aof_limit_rates(&t, p->max_rate);

    aof_vec3 measured = { rate_pitch, rate_yaw, rate_roll, 0.0f };
    aof_vec3 torque;
    aof_pid_update3(&c->pitch, &c->yaw, &c->roll,
                    &t, &measured, dt, p->use_dom, &torque);

    *out_pitch = torque.x;
    *out_yaw   = torque.y;
    *out_roll  = torque.z;
}
