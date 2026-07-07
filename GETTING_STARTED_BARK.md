# Getting Started — Bark (free, no Apple Developer account)

A complete, beginner-friendly walkthrough that takes you from **nothing installed** to **a test
notification landing on your iPhone** — for **$0**, with no Apple Developer account, using the free
[Bark](https://apps.apple.com/us/app/bark-custom-notifications/id1403753865) app as the delivery
hop instead of Apple's APNs.

This is the guide for **free-Apple-ID sideloads** (which can never receive native APNs pushes) and
for anyone who doesn't want to pay for the Apple Developer Program. If you *do* have a paid Apple
Developer account and want real native push (including Live Activities), follow the
[native APNs guide](GETTING_STARTED.md) instead — each guide is self-contained, so pick one and
follow it top to bottom.

> **Time & cost:** plan for **30–45 minutes**. Everything in this guide is free.

**What you give up versus native APNs** (so there are no surprises later):

- **Live Activities don't work** — they're APNs-only.
- **Notification content transits the Bark relay in plaintext.** You can self-host the relay
  (covered below) so it never touches Bark's hosted server, but either way it's one more hop than
  a direct APNs push.
- Your Bark push URL is a **bearer capability** — anyone who has it can send notifications to your
  phone, so treat it like a password.

Everything else works: inbox replies, mentions, private messages, subreddit/user watchers, Apollo's
notification sounds and icons, and tapping a notification deep-links straight into Apollo.

## Table of contents

- [0. What you're building & what you'll need](#0-what-youre-building--what-youll-need)
- [1. Install Docker](#1-install-docker)
- [2. Get the code](#2-get-the-code)
- [3. Configure your environment](#3-configure-your-environment)
- [4. Start the backend (with the Bark relay)](#4-start-the-backend-with-the-bark-relay)
- [5. Set up the Bark app & get your push URL](#5-set-up-the-bark-app--get-your-push-url)
- [6. Point Apollo at your backend & turn on Bark Delivery](#6-point-apollo-at-your-backend--turn-on-bark-delivery)
- [7. Verify end-to-end](#7-verify-end-to-end)
- [8. (Optional) Apollo's notification sounds in Bark](#8-optional-apollos-notification-sounds-in-bark)
- [9. (Optional) Open it up to the internet](#9-optional-open-it-up-to-the-internet)
- [10. Troubleshooting](#10-troubleshooting)

---

## 0. What you're building & what you'll need

The original [Apollo for Reddit](https://apolloapp.io/) app had a backend that delivered push
notifications (inbox replies, mentions) and ran subreddit/user *watchers*. That backend was shut
down in 2023. **This project is a self-hostable revival of it.**

Paired with **[the Apollo-Reborn tweak](https://github.com/Apollo-Reborn/Apollo-Reborn)**
(the tweak that lets a sideloaded Apollo build use *your own* Reddit credentials), running this
backend brings notifications and watchers back to life. You run one copy of this backend for
yourself (and optionally a few friends on the same build) — it's **single-tenant by design**.

On the Bark path, the backend doesn't talk to Apple at all. It POSTs each notification to your
device's **Bark push URL** — either a `bark-server` relay you run yourself (bundled in this repo's
Docker stack) or Bark's hosted `api.day.app` — and Bark gets it onto your phone using its own App
Store push entitlement:

```
  ┌─────────────┐    watched    ┌──────────┐    push    ┌──────────────┐            ┌──────┐
  │  Reddit API │ ────────────▶ │   THIS   │ ─────────▶ │ bark-server  │ ─────────▶ │ Bark │ ──▶ 📱
  └─────────────┘               │ BACKEND  │   (HTTP)   │ (self-hosted │  (Bark's   │ app  │ opens
                                └──────────┘            │ or day.app)  │   APNs)    └──────┘ Apollo
                                 Docker on your         └──────────────┘
                                 Mac / server / VPS
```

### Hard prerequisites (don't skip these)

These four things are **required**. Skipping any of them produces failures that look like backend
bugs but aren't — so confirm each one before you start.

1. **Your own custom bundle ID — never `com.christianselig.Apollo`.** Reddit's edge firewall blocks
   the original Apollo bundle ID: any request whose User-Agent contains that string gets a `403`
   "blocked by network security" page. You must re-sign your build under your own ID (e.g.
   `com.yourname.Apollo`) and use that ID *everywhere*. (Unlike the APNs path, a **wildcard signing
   profile is fine** here — the default from tools like Sideloadly — because Bark doesn't need the
   push entitlement. No Apple portal setup of any kind is required.)

2. **A sideloaded Apollo build re-signed under that bundle ID, with the
   [Apollo-Reborn tweak](https://github.com/Apollo-Reborn/Apollo-Reborn) installed**
   and its Reddit Custom API already working (i.e. you can already browse Reddit in the app using
   your own API key). This guide picks up *after* that part is working.

3. **The free [Bark app](https://apps.apple.com/us/app/bark-custom-notifications/id1403753865)**
   installed from the App Store on the same iPhone. Bark is a tiny open-source app whose only job
   is receiving pushes sent to its URL — it's the last hop that gets notifications onto your phone.

4. **A machine to run the backend on that stays powered on** — a home server, an always-on Mac, a
   mini-PC, a Raspberry Pi, or a cloud VPS (there's even a
   [free option on Oracle Cloud](ORACLE_CLOUD.md)). For notifications to keep arriving, this
   machine and its Docker containers need to keep running.

---

## 1. Install Docker

The backend ships as a set of Docker containers, so the only thing you install on the host is Docker
itself. Pick your platform.

<details open>
<summary><strong>macOS</strong></summary>

1. Download **Docker Desktop** from
   [docker.com/products/docker-desktop](https://www.docker.com/products/docker-desktop/) (choose the
   Apple Silicon or Intel build to match your Mac).
2. Open the `.dmg` and drag **Docker** into **Applications**.
3. Launch Docker from Applications and let it finish starting (the whale icon in the menu bar stops
   animating when it's ready). Accept the permission prompts.
4. For long-term stability, open **Docker Desktop → Settings → General** and enable **"Start Docker
   Desktop when you sign in"** so the backend comes back after a reboot.
5. Verify in Terminal:
   ```bash
   docker --version
   docker compose version
   ```
   Both should print a version number.

</details>

<details>
<summary><strong>Linux</strong> (recommended for an always-on server)</summary>

1. Install Docker Engine + the Compose plugin with the official convenience script:
   ```bash
   curl -fsSL https://get.docker.com | sh
   ```
   (Or follow your distro's instructions at [docs.docker.com/engine/install](https://docs.docker.com/engine/install/).)
2. Let your user run Docker without `sudo`:
   ```bash
   sudo usermod -aG docker $USER
   # log out and back in (or run: newgrp docker) for this to take effect
   ```
3. Make sure Docker starts on boot — essential for an unattended server:
   ```bash
   sudo systemctl enable --now docker
   ```
4. Verify:
   ```bash
   docker --version
   docker compose version
   ```

</details>

<details>
<summary><strong>Windows</strong></summary>

Install **Docker Desktop** with the **WSL 2** backend — follow
[docs.docker.com/desktop/install/windows-install](https://docs.docker.com/desktop/install/windows-install/).
Then run the rest of this guide's commands from a WSL 2 terminal (Ubuntu). The commands are
identical to the Linux/macOS ones. Windows isn't the recommended host for an always-on deployment —
a Linux box or VPS is sturdier — but it works for testing.

</details>

---

## 2. Get the code

Clone the repository and move into it:

```bash
git clone https://github.com/Apollo-Reborn/apollo-backend
cd apollo-backend
```

You **do not need Go or any build tools** — the app image is built *inside* Docker from the code
you just cloned (that's what the `--build` in `make docker-up-bark` does). Building this way keeps
the running containers in sync with your checkout: after a `git pull`, the next
`make docker-up-bark` rebuilds automatically.

---

## 3. Configure your environment

Create your environment file:

```bash
cp .env.docker.example .env.docker
```

Now open `.env.docker` in any text editor. The Bark path needs almost nothing:

| Variable | Set it to |
|---|---|
| `APPLE_KEY_PATH`, `APPLE_KEY_ID`, `APPLE_TEAM_ID`, `APPLE_APNS_TOPIC` | **Leave all four empty.** That's what puts the backend in **Bark-only mode** — APNs stays disabled, no Apple credentials or `.p8` key are needed, and the `secrets/` folder can stay nonexistent. (Setting only *some* of the four is a startup error; all-empty is the supported free configuration.) |
| `REGISTRATION_SECRET` | A long random string of your choosing (e.g. the output of `openssl rand -hex 24`). This stops strangers from registering against your backend. Optional on a private LAN, **strongly recommended before you expose anything to the internet** ([Step 9](#9-optional-open-it-up-to-the-internet)). |

**About the Reddit credentials (`REDDIT_CLIENT_ID`, `REDDIT_CLIENT_SECRET`, `REDDIT_REDIRECT_URI`,
`REDDIT_USER_AGENT`):** you almost certainly already configured these inside the tweak's **Custom
API** screen when you got Reddit browsing working — the tweak sends them to the backend
automatically at registration time. These env vars are a **fallback** the backend uses if the tweak
fails to inject them. For a single-user setup it's harmless (and a good safety net) to fill them in
here too. If you do, the **User Agent must follow Reddit's format**, including your username:
`ios:com.yourname.Apollo:v1.0 (by /u/yourusername)`.

**Leave these defaults alone** (they're already correct for the bundled stack):
- The Postgres and Redis URLs point at the built-in containers. ⚠️ Don't append a query string (like
  `?sslmode=disable`) to `DATABASE_CONNECTION_POOL_URL` — the app appends its own `?pool_max_conns=…`
  and a second `?` makes the database driver reject the URL.

---

## 4. Start the backend (with the Bark relay)

Bring the whole stack up in the background, **including the self-hosted Bark relay**:

```bash
make docker-up-bark      # same as: docker compose --profile bark up -d --build
```

> **Planning to use Bark's hosted `api.day.app` instead of self-hosting the relay?** (See the
> trade-off in [Step 5](#5-set-up-the-bark-app--get-your-push-url).) Then you don't need the relay
> container — plain `make docker-up` is enough. Everything else in this guide is the same.

(The `--build` matters: plain `docker compose up` reuses the app image it built last time, so after
a `git pull` you'd silently keep running the old code. A rebuild with nothing changed takes
seconds thanks to layer caching.)

Then follow the logs until things settle:

```bash
make docker-logs    # Ctrl-C to stop following; the containers keep running
```

This starts everything the backend needs, each in its own container:
- **postgres** + **pgbouncer** — the database and its connection pooler
- **redis-queue** + **redis-locks** — two Redis instances (job queue and dedup locks)
- **migrate** — a one-shot that creates the database schema, then exits
- **api** — the HTTP server (port **4000**) the app talks to
- **scheduler** — decides what to check and when
- **worker-notifications / -subreddits / -trending / -users / -stuck-notifications** — do the work
- **bark-server** — the self-hosted [Bark](https://github.com/Finb/bark-server) relay (port
  **8080**, override with `BARK_SERVER_PORT`). Its device registrations persist in the `barkdata`
  Docker volume.

### Confirm it's healthy

```bash
curl http://localhost:4000/v1/health
```

You should get:

```json
{"status":"available"}
```

And in the logs you should see the line confirming Bark-only mode (this is expected, not an error):

```
APNs disabled (no APPLE_* env vars set)
```

🎉 If you see both, the backend is up.

> **If the `api` or worker containers keep restarting:** run `make docker-logs` and read the error —
> the process logs the exact problem and exits. The classic cause is a *partial* `APPLE_*` config
> (some of the four set, some empty). For the Bark path, all four must be **empty**.

---

## 5. Set up the Bark app & get your push URL

Install the free [Bark app](https://apps.apple.com/us/app/bark-custom-notifications/id1403753865)
from the App Store, then decide where your notifications will relay through:

- **Self-hosted `bark-server` (recommended)** — the container you just started. Notification
  content never touches a third-party server; it goes from your backend to your relay to Apple.
- **Bark's hosted `api.day.app`** — zero extra setup (it's the Bark app's default server), but your
  notification content (usernames, message previews) transits Bark's server **in plaintext**.

**Self-hosted:**

1. In the Bark app, go to the **service list / server screen** and **add a server**.
2. Enter your relay's address: `http://<server-lan-ip>:8080` while testing on your home network
   (find the server's LAN IP with `ipconfig getifaddr en0` on macOS or `hostname -I` on Linux), or
   your public HTTPS URL once you've [exposed it](#9-optional-open-it-up-to-the-internet).
   ⚠️ Not `localhost` — this address must be reachable both **from your phone** (to register) and
   **from the backend's containers** (to deliver).
3. The app registers itself and shows your **push URL**: `http://<server>:8080/<device-key>`.
   Copy it — you'll paste it into Apollo in the next step.

**Hosted `api.day.app`:**

1. Open the Bark app — it registers against `api.day.app` automatically on first launch.
2. Copy the push URL it shows: `https://api.day.app/<device-key>`.

Either way, allow notifications when Bark asks (**iOS Settings → Bark → Notifications** if you
missed the prompt), and remember: the push URL is a **bearer capability** — anyone who has it can
push to your phone. Don't post it anywhere.

---

## 6. Point Apollo at your backend & turn on Bark Delivery

On your iPhone, in Apollo (with the tweak installed):

1. Go to **Settings → Custom API → Notification Backend**.
2. Set:
   - **Backend URL**:
     - For testing on the **same Wi-Fi** as the server: `http://<server-lan-ip>:4000`.
     - Once you've [exposed it to the internet](#9-optional-open-it-up-to-the-internet): your
       `https://your.domain` URL.
   - **Registration Token**: the same value you set for `REGISTRATION_SECRET` (leave blank if you
     didn't set one).
3. Tap **Test Connection**. This hits `GET /v1/health` on your backend — a success here means the
   app can reach it.
4. Turn on **Bark Delivery** and paste your push URL from [Step 5](#5-set-up-the-bark-app--get-your-push-url)
   into **Bark Push URL**.
5. Tap **Test Bark Notification**. This sends a notification straight from the app through your
   Bark URL — if it shows up, the Bark half of the chain works.

While you're here, double-check the **main Custom API screen** has your **Reddit API Key**,
**Redirect URI**, and **User Agent** filled in for *this* bundle ID (you needed these to get Reddit
browsing working at all). The notification backend reuses them.

Finally, **turn notifications on**:
- Inside Apollo, enable inbox notifications for your account (and add any subreddit/user **watchers**
  you want).
- Relaunch Apollo once so it registers with the backend. (On a build without the push entitlement,
  the tweak steps in automatically: it gives Apollo a synthetic device token so the *entire* native
  notifications and watchers UI works as if APNs were available, and registers the device with your
  backend as a Bark device.)

Once registration succeeds, the backend sends a **"hello, is this thing on?"** confirmation
notification — a quick sign the whole chain is wired up end-to-end.

---

## 7. Verify end-to-end

Let's confirm a real backend-originated notification reaches your phone (the in-app test button in
Step 6 only tested the Bark half; this tests the whole pipeline).

### 7a. Find your device's token

Open a database shell:

```bash
make docker-psql    # same as: docker compose exec postgres psql -U apollo apollo
```

Then run:

```sql
SELECT id, transport, transport_endpoint, apns_token FROM devices ORDER BY id DESC LIMIT 1;
```

You should see one row with **`transport = bark`** and your push URL in `transport_endpoint`. (The
`apns_token` column holds the tweak's synthetic 64-hex token — on the Bark path it's just the
device's ID, never sent to Apple.) Copy the `apns_token` value. While you're here, confirm the
account got linked:

```sql
SELECT a.username, a.check_count, a.last_message_id, da.inbox_notifiable
FROM accounts a
JOIN devices_accounts da ON da.account_id = a.id;
```

Type `\q` to exit psql.

### 7b. Send a test push

```bash
curl -X POST http://localhost:4000/v1/device/<token>/test/post_reply
```

Expect a `200` response and a notification on your phone within a second or two — with Apollo's
icon, and tapping it should open Apollo. Other test types you can swap in for `post_reply`:

```
comment_reply   private_message   username_mention   subreddit_watcher   trending_post
```

### 7c. The first-message "warmup" gotcha

The **first real inbox message after registration won't notify you.** On its first poll the worker
silently records your latest message ID and marks the account warmed up (`check_count = 1`); the
*second* message onward push normally. This is expected — not a bug.

If you'd rather not wait for a throwaway first message, skip warmup manually:

```bash
make docker-psql
```
```sql
UPDATE accounts SET check_count = 1 WHERE username = '<your-reddit-username>';
```

---

## 8. (Optional) Apollo's notification sounds in Bark

By default, Bark notifications play the standard iOS alert tone. To hear the notification sound you
picked in Apollo's settings instead, import it into the Bark app once:

1. Download the matching `.caf` file from
   [Apollo-Reborn's `assets/bark-sounds`](https://github.com/Apollo-Reborn/Apollo-Reborn/tree/main/assets/bark-sounds)
   (files are named by Apollo's internal sound ids, e.g. `diabolicalDoorbell.caf`).
2. In the Bark app: **Service tab → the "Alert Sound" card → "Click here to view all available
   sounds." → Upload Sound**.

The tweak keeps the sound pin in sync with your in-app pick automatically; sounds you haven't
imported just fall back to the default tone, so skipping this breaks nothing.

**Icons need no setup**: notifications show the post's thumbnail when there is one, and Apollo's
app icon otherwise — including the alternate icon you've selected in Apollo, if any.

---

## 9. (Optional) Open it up to the internet

So far everything works while your phone is on the **same network** as the server. For
notifications to keep working when you're on cellular or away from home, your **backend** (port
4000) needs to be reachable from the internet — and if you self-host `bark-server`, ideally that
too (port 8080), so the Bark app can stay registered from anywhere.

The options — a VPS with a Caddy reverse proxy, a home server with port-forwarding + dynamic DNS,
or a Cloudflare Tunnel (the easiest from home, and the only one that works behind CGNAT) — are
**identical for the APNs and Bark paths**, so they're documented once, in the native guide:

➡️ **[Step 8 of the native APNs guide](GETTING_STARTED.md#8-optional-open-it-up-to-the-internet)**
walks through all three. Everything there applies verbatim here; where it proxies port `4000`, add
a second hostname or route for port `8080` if you self-host the relay.

Two Bark-specific notes:

- **Set `REGISTRATION_SECRET` before exposing anything** ([Step 3](#3-configure-your-environment))
  — on the Bark path this matters *extra*, because a device's push URL is an address your workers
  will POST to. Open registration on a public backend would let a stranger point your workers at
  arbitrary URLs.
- After exposing, update the **Bark app's server address** and re-paste the new `https://…/<device-key>`
  push URL into Apollo's **Bark Push URL**, then update Apollo's **Backend URL** — all three move
  to the public hostname.

Likewise, the long-term care & feeding — surviving reboots, backing up the database, updating,
monitoring — is transport-agnostic and lives in
**[Step 9 of the native APNs guide](GETTING_STARTED.md#9-keep-it-running-long-term)**. One Bark
addition: the `barkdata` volume holds the relay's device registrations — `make docker-nuke` deletes
it (along with everything else), after which the Bark app needs to re-add the server.

---

## 10. Troubleshooting

Start with `make docker-logs` — most problems announce themselves there. Common issues, roughly in
the order you'd hit them:

| Symptom | Likely cause | Fix |
|---|---|---|
| `curl .../v1/health` refuses the connection | Containers still starting, or one crashed | Wait a few seconds; then `make docker-logs` to see what failed |
| `api` / worker containers restart-loop | A *partial* `APPLE_*` config (some set, some empty) | Read the exact error in `make docker-logs`; for the Bark path, empty **all four** `APPLE_*` vars, then `make docker-up-bark` |
| App's **Test Connection** fails, but `curl localhost:4000/v1/health` works on the server | Phone can't reach the server: wrong IP/port, different network, or firewall | Use the server's LAN IP (not `localhost`); confirm phone and server are on the same Wi-Fi (or finish [Step 9](#9-optional-open-it-up-to-the-internet)) |
| **Test Bark Notification** does nothing | Wrong push URL, Bark app not allowed to notify, or the phone can't reach the relay | Re-copy the push URL from the Bark app; check **iOS Settings → Bark → Notifications**; confirm the relay address isn't `localhost` |
| In-app test works, but the [Step 7b](#7b-send-a-test-push) `curl` test doesn't | The **backend containers** can't reach your push URL (the phone reaching it isn't enough) | Use a LAN IP or public hostname in the push URL, never `localhost`; check `docker compose logs worker-notifications` for `bark` errors |
| Device row has `transport = apns` instead of `bark` | **Bark Delivery** was off (or the push URL empty) when Apollo registered | Turn on **Bark Delivery**, paste the **Bark Push URL**, relaunch Apollo, re-check the `devices` row |
| Registration fails with `401` | **Registration Token** in the app doesn't match `REGISTRATION_SECRET` | Make them identical (or clear both while testing on a private LAN) |
| Registration fails with `422` | Bark device registered without a push URL | Fill in **Bark Push URL** before relaunching |
| `403 "blocked by network security"` HTML on Reddit calls | Bundle ID / User-Agent still contains `com.christianselig.Apollo` | Re-sign under your own bundle ID; fix the tweak's User Agent |
| `oauth revoked` right after a *successful* token refresh | User Agent missing the `(by /u/yourname)` suffix | Use a UA like `ios:com.yourname.Apollo:v1.0 (by /u/you)` |
| First inbox message didn't notify | Expected warmup behavior | The second message onward push; or run the `UPDATE accounts SET check_count = 1` shortcut ([7c](#7c-the-first-message-warmup-gotcha)) |
| Notifications stop when you leave home | Backend isn't reachable from the internet | Complete [Step 9](#9-optional-open-it-up-to-the-internet) |
| Live Activity won't start / `POST /v1/live_activities` returns 422 | Live Activities require APNs — they can't work in Bark-only mode | Expected; native push needs the [APNs path](GETTING_STARTED.md) |
| Apollo's notification sound doesn't play | The matching `.caf` isn't imported into Bark | Import it ([Step 8](#8-optional-apollos-notification-sounds-in-bark)); until then the default tone plays |

For the rarer, deeper failure modes (TLS fingerprint EOFs, the JWT column-length migration, the
`raw_json` WAF quirk), see the [Troubleshooting table in the README](README.md#troubleshooting).

---

**Get a paid Apple Developer account later?** You can switch to native push without redoing any of
this: follow [Step 3 onward of the APNs guide](GETTING_STARTED.md#3-set-up-apple-app-id-apns-key-team-id)
to fill in the `APPLE_*` values, re-sign your build with an explicit push-enabled App ID, and turn
**Bark Delivery** off in Apollo's settings — the device re-registers over APNs in place. (And on a
paid build you can flip back and forth freely.)
