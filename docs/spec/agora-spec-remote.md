# agora spec — remote control (MLE control plane)

Operator direction (2026-07-15): **"remote" = anything that allows control.** agora is the central harness reused wherever funnel/bridle would be; every controlling connection gets the same security model regardless of where it comes from. Connections are secured with **MLE (message-level encryption)** — trust never rides on the transport or a relay. **External traffic goes through the interchange**, and MLE is **keyed off the remote end's device key, passkey-style**.

Grounded in extraction from codex (2026-07-15): their app-server remote-control (System A: pairing-code UX, device list/revoke — but security = TLS-to-chatgpt only, zero on-device crypto, and paired devices get full unscoped control) and their exec-server remote mode (System B: Noise hybrid-IK end-to-end over a dumb relay). agora takes **B's crypto + A's pairing UX + the capability layer codex deliberately omitted**.

## 1. Definitions & topology

A **controller** is any client that can send input/approvals to a daemon (TUI, webpage, vessel, another agora, broker dispatch). Three paths, one trust model:

| path | transport | MLE? |
|---|---|---|
| same-host | UDS | exempt (OS user boundary is the auth) — the only exemption |
| tailnet | ws | **yes** — tailscale is defense-in-depth, not the trust root |
| external | ws **via the interchange** | **yes** — interchange is a dumb relay |

The interchange relays by `stream_id` only; acks, sequence numbers, chunking, reassembly are endpoint-owned (codex relay pattern — proven in both their systems). The relay can be lost, replaced, or hostile without affecting confidentiality/integrity: it never sees plaintext and cannot impersonate either end.

## 2. MLE layer

- **Noise IK**, hybrid suite target `X25519+ML-KEM-768 / AESGCM / SHA-256` (codex exec-server's exact suite — post-quantum hybrid). Go gating question: ML-KEM hybrid support in flynn/noise; **fall back to classical `Noise_IK_25519_AESGCM_SHA256` for v1** with the suite negotiated in the enrollment record so upgrading is a re-enrollment, not a protocol break.
- **Static keys = identity — literally the identity primitive from the index spec.** The daemon's static key is its *instance identity* (who acts); a controller device's static key is a *device identity*, `kind = "device"` — same keypair-is-identity model, same fingerprint ids, same petname rules, same DID alignment, same keyring storage. There is ONE key/identity/enrollment system in agora; remote control is just the (device identity → daemon identity) binding of it. Device keys are passkey-style: generated on-device, private key never leaves it (secure enclave where offered; file fallback 0600). An **enrollment is a binding**: the daemon certifies "device X may control me with capabilities Y" — VC-shaped like every other binding, revocable independently of the device key itself. One device key may be enrolled with many daemons (one binding per daemon, same fingerprint everywhere).
- **IK direction**: controller initiates and **pins the daemon's static key** (learned at enrollment). First message payload carries a short-lived session-authorization token. Daemon recovers the controller's static key from the handshake and completes **only if that key is enrolled and not revoked** (codex's registry-validation step, with the daemon's own device registry as the authority — no third party).
- **Prologue binding**: `(daemon_id, device_id, stream_id, channel-epoch)` length-prefixed into the handshake — cross-binding/replay protection.
- DoS bounds at the daemon: max concurrent handshakes, max failed handshakes per source, pending-validation timeout (codex relay numbers as starting points).
- Everything above the handshake is the normal session protocol (agora-spec-io.md) — MLE wraps the same event stream; nothing forks.

## 3. Enrollment (pairing, passkey-style)

Borrow codex System A's UX verbatim; replace its trust model:

1. Operator (on an already-trusted surface: local TUI or same-host CLI) runs `agora device pair` → daemon issues `{pairing_code, manual_code, expires_at}` (short TTL, single-use). Display as QR + short string.
2. New device claims the code over its chosen path (interchange for external). The **claim carries the device's static public key + metadata** `{display_name, device_type, platform, os_version, app_version}`.
3. Trusted surface shows the claim (name + key fingerprint) → **operator approves** (this is the passkey ceremony's attestation moment — human confirms the enrolling device). Poll `pairing/status → {claimed}` on the device side.
4. Daemon persists the enrollment: `{device_id, static_pubkey, metadata, capabilities, enrolled_at}`; device pins the daemon's static pubkey. Both sides now hold what IK needs.

Device management: `device list` / `device revoke` (revocation kills live sessions keyed by that device and refuses future handshakes), `last_seen_at` tracking. Desired-state model: remote control enabled/disabled persistently per daemon + ephemeral CLI override (`--remote-control on|off`) — codex's watch-channel pattern.

## 3a. Browser device-key custody (the webpage as a device)

The chat webpage is a device like any other; the question is where its key lives.

- **Primary**: device key generated via WebCrypto (X25519, `extractable: false` — supported in current Chrome/Firefox/Safari), persisted as a CryptoKey in IndexedDB. Noise IK runs in a **WASM module compiled from the same Go MLE package** — one implementation everywhere, no JS re-implementation to drift; ws frames carry binary ciphertext.
- **Fallback** (no X25519 WebCrypto): key generated in WASM, stored encrypted-at-rest under a WebCrypto AES key. Weaker custody — the enrollment records `key_custody: software`, visible in `device list`, so the operator knows which devices hold softer keys.
- Browser storage cleared ⇒ device key gone ⇒ re-pair. Correct behavior, not a bug (matches lost-phone semantics).
- **Named deviation** (explicit config only, never default): kiosk-style local/tailnet webpage over TLS + herald session auth instead of MLE — for the "trusted living-room screen" case; recorded in the daemon config as a deviation, capability-capped at observer+interactive (no approver without MLE).

## 4. Capabilities (the layer codex omitted — required here)

Enrollment assigns scopes; the session inherits them; every inbound message is checked against them (defense-in-depth on top of "only enrolled keys handshake"):

- `observer` — receive events only.
- `interactive` — user_message / steer / interrupt; also answers `question`-kind cards (structured answers — approvals §3; gates/permissions still need `approver`).
- `approver` — answer approval.requested for permission/`plan`/`gate` kinds (can be granted without `interactive`: a phone that can approve but not steer).
- `admin` — device management, profile switching, daemon config.

Plus optional constraints per device: allowed profiles, allowed threads (e.g. vessel bound to the chat profile only), approval kinds (exec yes, patch no). Codex's finding is the warning: their paired device is silently a fully-privileged client incl. approvals — never replicate that default. agora default for a new device = `observer + approver`, upgrade explicit.

## 5. Approvals & presence over remote

Same events as agora-spec-io.md (fan-out, first-answer-wins, `approval.resolved {by}`) — with `by` = device identity, and answer rights gated by the `approver` capability. Presence events carry device metadata so the TUI shows *which* device is attached. Every remote-originated input is attributable in the transcript (device_id on the item) — the audit trail falls out of the identity model.

## 6. Relationship to the rest of the stack

- **Interchange** = the external rendezvous/relay. Needs only: accept ws, route frames by stream_id, drop on TTL. Registration of a daemon with the interchange authenticates with the daemon's key (challenge-response), not a bearer.
- **Nexus/broker dispatch** is a *different* trust domain (aspect keyfiles/JWT per nexus-auth) — dispatch envelopes arrive over the broker channel as today. A broker-driven agora session and a device-driven one meet at the same daemon; capabilities keep them separated. Long-term the broker channel can migrate onto the same MLE layer (aspect key = static key), unifying the two — not v1.
- **Herald/passkeys**: the dormant herald IdP and the dashboard passkey login are *human web auth*; device enrollment here is machine-key auth with a human ceremony. If herald wakes, it can become the enrollment approver surface, but nothing depends on it.
- Codex trap noted for the record: their `Feature::RemoteControl` is marked Removed while the capability ships behind startup-mode + policy — ground on protocol code, not flags. (Their protocol surface: `remoteControl/{enable,disable,status/read,pairing/start,pairing/status,client/list,client/revoke}` — agora mirrors this verb set.)

## 6a. The dispatch-controlled pod (operator, 2026-07-15 — the real "nobody attached" case)

The primary headless scenario is not a phone catching up — it's the **dispatch system controlling a fully-specced agora pod**. One generic agora image; dispatch makes it specific at attach time. This realizes the dispatch-native architecture (all agents = dispatch pods) with agora as the pod runtime.

- **Pod = the same binary**, `agora daemon --pod`: boots blank — auto-gen'd local key (so it can do MLE) but no working identity, no profile, no workspace; refuses turns until provisioned. Its only enrolled controller is the **dispatch/broker identity** (binding baked at pod build or injected as a k8s secret: broker pubkey + capability grant `admin + interactive + observer`). Dispatch is a controller like any device — same MLE, same attribution, same revocation.
- **`provision` message** (admin capability, over the normal session protocol):
  ```jsonc
  {
    "type": "provision",
    "identity":  { "source": "keyring:nexus/anvil" | "herald:… (provision, ephemeral)" },
    "profile":   "aspect-builder",
    "model_aliases": { "frontier": "…", "local-fast": "…" },   // overlay
    "session":   { "new": true } | { "resume": "<thread_id>" },
    "workspace": { "dir": "/work", "checkout": { "repo": "…", "ref": "…" } },  // cairn/git
    "context":   { "files": ["…"], "fragments": ["…"],
                   "mcp_overlay": { "comms": { … } },          // resolves {identity} = anvil
                   "env": { … } }
  }
  ```
  Provisioning is atomic: apply-all-or-reject, then `provisioned {identity_fp, profile}` event. **This is why identity/profiles/interpolation are parameters**: the pod becomes *anvil wearing aspect-builder with anvil's comms channel* purely through control-plane data — zero per-aspect images or configs.
- **Then dispatch drives it** like any interactive client: `user_message` = the ticket/task; events stream back (broker tracks progress via the same event stream — no side-channel status protocol); approvals raised by the pod queue per §8 and **escalate through dispatch to the operator** (this is P3c's escalation frame, landing naturally: the approval event forwards up the control chain with full attribution).
- **Lifecycle**: `deprovision` clears identity/session/workspace back to blank (warm-pool reuse — cheaper than pod churn), or the pod dies with the task (ephemeral identity mode fits here: herald-provisioned short-TTL keys expire with it). `session.resume` enables handoff: a pod can die mid-task and a fresh pod resumes the thread from the thread store.
- Multiple concurrent controllers still work: dispatch (admin) driving while the operator's TUI attaches as observer to watch a builder live — the multi-attach model from the io spec, unchanged.

## 7. Interchange rendezvous protocol (sketch)

The external relay, kept deliberately dumb:

- **Daemon registration**: daemon connects outbound ws to the interchange, authenticates by challenge-response with its identity key (interchange sends nonce, daemon signs; no bearer tokens), and holds the connection open as its *mailbox*. Registration record: `{daemon_fingerprint, connected_at}`. Re-registration replaces (single active mailbox per fingerprint).
- **Client connect**: controller connects ws, sends `{target: <daemon_fingerprint>, stream_id}`. Interchange matches target → mailbox and from then on blindly forwards frames both ways for that stream_id. Unknown target → immediate close with code (client backs off; enables daemon-offline detection).
- **Frame format**: `{stream_id, seq, flags(chunk/end/ack), payload}` — payload is opaque MLE ciphertext from frame one (the IK handshake itself rides in frames). Endpoint-owned reliability: acks + retransmit + reassembly at the ends; interchange buffers nothing beyond a small in-flight window and drops streams idle past TTL (~2 min).
- **What the interchange can do at worst**: drop/delay/reorder traffic and observe metadata (which fingerprints talk, volume, timing). It cannot read, modify, or spoof — MLE guarantees that. Metadata privacy is explicitly out of scope for v1.1 (it's shadow's own interchange).
- Deployment: interchange = small stateless Go service (k3s pod on dMon), horizontal-scale-safe if registration moves to shared state later. Tailnet clients bypass it entirely.

## 8. Disconnected operation — approvals & push

The mobile reality: usually *nothing* is attached when an approval fires.

- **Canonical approval pipeline** (one ordering, all surfaces): 1. PermissionRequest hooks (may auto-allow/deny — hooks spec §2.2) → 2. profile approval policy (may auto-decide) → 3. fan-out to attached `approver` clients (first answer wins) → 4. queue + push doorbell → 5. `approval_timeout` fallback (default deny). Every stage's decision is attributed and recorded.
- **Approvals queue at the daemon** (they already do — the bottom-pane queue generalizes): `approval.requested` events wait for an approver-capable client. New: per-profile `approval_timeout` with a policy fallback — default **deny after 15 min** with the timeout recorded as the decision reason (never auto-approve on timeout; profiles on the `never-escalate` preset (approvals §2) can set `fallback = "fail-turn"`). **Exception: kind `question` never timeout-denies — it PARKS** (thread `waiting-on-answer`, card stays queued; agora-spec-planning-questions §5–6); `plan` follows the deny default (a denied gate just re-enters the revision loop, nothing mutated).
- **Push notification path**: on queuing an approval (and optionally on turn-complete / needs-attention), the daemon fires a `notify` hook-style outbound: pluggable notifier (ntfy/webhook/webpage web-push/vessel speak) carrying only *"agora: approval pending (exec) — <thread petname>"* + a deep link. **Never the command/diff content** — content requires an attached MLE session to view. Notifier config is per-daemon; the notification is a doorbell, not a channel.
- **Reattach replay** (io spec) covers the gap: device attaches, replays tail, sees the pending approval with full context, answers. `approval.resolved {by}` then fans out as normal.

## 9. Reconnection & flaky links

- IK is 1-RTT and cheap: **re-handshake on every reconnect** (no session-ticket resumption in v1 — less state, fewer invariants; the prologue epoch increments so old sessions can't be replayed into new ones).
- Liveness: MLE-level ping/pong (~25s, under typical NAT timeouts); client-side exponential backoff with jitter on reconnect.
- Gap handling: events carry per-thread seq; on reattach the client sends `last_seq` and the daemon replays from there (bounded window; beyond it → full-tail replay). Input side is naturally idempotent-ish (user_message dupes are visible; approval_response carries the approval id, dupes are no-ops after first-answer-wins).
- Mid-turn disconnect of the *only* interactive client changes nothing: the daemon owns the turn (io spec) — it runs to completion or to an approval, which then queues per §8.

## 10. Build order

v1: UDS exemption + tailnet MLE (classical IK) + pairing flow + capabilities + device list/revoke + approval queue/timeout + reconnect/replay (§9). v1.1: interchange relay path (§7) + push notifier (§8). Later: hybrid PQ suite, broker-channel unification, herald-surfaced enrollment, metadata privacy on the relay.
