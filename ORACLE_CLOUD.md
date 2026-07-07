# Free hosting on Oracle Cloud (Always Free tier)

> **Status: first draft.** This walkthrough is community-requested and hasn't had as many
> real-world runs as the main guides yet — if something doesn't match what you see, please open an
> issue.

Oracle Cloud's [Always Free tier](https://www.oracle.com/cloud/free/) is the one mainstream cloud
that gives away a VM big enough to run this backend comfortably, forever, for $0: up to **4 Ampere
A1 (ARM) CPU cores and 24 GB of RAM**, 200 GB of storage, and 10 TB/month of egress. The whole
apollo-backend stack idles at a fraction of that.

This guide gets you from no Oracle account to a VM that's ready for the normal Getting Started
flow. It replaces the "rent a VPS" part of
[Step 8a of the APNs guide](GETTING_STARTED.md#8a-vps--caddy-reverse-proxy-most-reliable) — the
backend setup itself is unchanged, so you'll bounce back to a main guide
([APNs](GETTING_STARTED.md) or [Bark](GETTING_STARTED_BARK.md)) once the VM is up.

**ARM works.** The Always Free VM is ARM64 (Ampere A1), and the entire Docker stack runs natively
on it — the app image is built from source inside Docker (Go cross-compiles cleanly), and every
bundled image (`postgres`, `redis`, `pgbouncer`, `bark-server`, and the prebuilt
`ghcr.io/apollo-reborn/apollo-backend` image) is published for `linux/arm64`. No flags or
workarounds needed.

## The three catches (read before signing up)

Free comes with strings. None are dealbreakers, but knowing them up front saves frustration:

1. **A1 capacity is scarce for free accounts.** Creating an Always Free A1 instance often fails
   with **"Out of capacity for shape VM.Standard.A1.Flex"**, especially in popular regions. Free
   accounts only get capacity in their **home region**, which is chosen at signup and **can never
   be changed** — so pick a less-crowded region if you can, and expect to retry instance creation
   (sometimes for days).
2. **Idle Always Free instances can be reclaimed.** Oracle stops Always Free compute it deems idle
   (roughly: over a 7-day window, 95th-percentile CPU, network, *and* memory utilization all under
   ~20% — see [Oracle's policy](https://docs.oracle.com/en-us/iaas/Content/FreeTier/freetier_topic-Always_Free_Resources.htm)).
   A single-user apollo-backend is light enough that this can genuinely happen.
3. **Both problems disappear if you upgrade to Pay As You Go.** Upgrading puts a credit card on
   file but changes nothing about the price — Always Free resources **stay free** on a PAYG
   account, you just gain the ability to be billed if you exceed them. PAYG accounts get normal
   capacity priority (no more "out of capacity") and are exempt from idle reclamation. If you're
   comfortable trusting yourself not to click "create" on paid resources, upgrading after signup
   is the single best quality-of-life move on this platform. If not, set a budget alert either way.

## 1. Sign up

1. Create an account at [oracle.com/cloud/free](https://www.oracle.com/cloud/free/). You'll need a
   credit card for identity verification even on the free tier — it isn't charged unless you
   explicitly upgrade *and* exceed the free allowances.
2. **Choose your home region carefully** (see catch #1 — it's permanent). Any region works
   functionally; the backend doesn't care where it runs, and notification latency is dominated by
   Reddit polling intervals, not geography.

## 2. Create the VM

In the Oracle Cloud console: **Compute → Instances → Create instance**.

- **Image**: **Ubuntu 24.04** (the `aarch64` build is selected automatically with the A1 shape).
  Prefer Ubuntu over the default Oracle Linux — the rest of the project's docs assume
  Debian-family commands.
- **Shape**: click *Change shape* → **Ampere** → **VM.Standard.A1.Flex**. Allocate **2 OCPUs and
  12 GB RAM** — half the free allowance, far more than the backend needs, and it leaves room for a
  second free instance later. (Going straight to 4/24 is fine too.)
- **Networking**: keep the defaults (a new VCN with a public subnet), and make sure **"Assign a
  public IPv4 address"** is on.
- **SSH keys**: add your public key (or download the generated pair — it's your only way in).
- **Boot volume**: the default (~47 GB) is plenty.

Click **Create**. If you get **"Out of capacity"**, see catch #1: retry later (capacity is
released continuously), try a different availability domain in the dropdown if your region has
several, reduce to 1 OCPU / 6 GB, or upgrade to PAYG.

Once it's running, note the **public IP** and connect:

```bash
ssh ubuntu@<public-ip>
```

## 3. Open the firewalls (both of them)

This is the step that trips everyone up on Oracle: there are **two firewalls**, and both block
inbound traffic by default. Opening only one looks like a mysterious timeout.

### 3a. The cloud firewall (VCN security list)

**Networking → Virtual cloud networks → your VCN → Security Lists → Default Security List → Add
Ingress Rules.** Add one rule per port:

| Source CIDR | IP Protocol | Destination Port | Purpose |
|---|---|---|---|
| `0.0.0.0/0` | TCP | `80` | HTTP (Caddy's certificate challenge + redirect) |
| `0.0.0.0/0` | TCP | `443` | HTTPS (the reverse-proxied API) |

Port 22 (SSH) is already open by default. **Don't add port 4000** — the API should only be
reached through the HTTPS reverse proxy. (Same for 8080 if you self-host `bark-server`; give it a
hostname behind the proxy instead, as noted in the Bark guide.)

### 3b. The host firewall (iptables — not ufw)

Oracle's Ubuntu images ship with **iptables rules that reject all inbound traffic except SSH**,
stored in `/etc/iptables/rules.v4` and restored on boot by `netfilter-persistent`. This is *in
addition to* Ubuntu's usual defaults, and it's why "I opened the security list and it still
doesn't work" is the most common Oracle complaint. **Don't use `ufw` here** — it fights with the
pre-installed rules; edit iptables directly:

```bash
# insert ACCEPT rules for 80/443 above the blanket REJECT rule
sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 80 -j ACCEPT
sudo iptables -I INPUT 6 -m state --state NEW -p tcp --dport 443 -j ACCEPT

# persist them across reboots
sudo netfilter-persistent save
```

(The `6` inserts at position 6, which on the stock rule set lands just after the existing SSH
ACCEPT and before the final REJECT. If you've modified the rules, check with
`sudo iptables -L INPUT --line-numbers` and pick the right position.)

> **Note on Docker**: containers published with `ports:` (like the API's `4000`) bypass the INPUT
> chain via Docker's own NAT rules, so port 4000 ends up reachable *if* the VCN security list
> allows it — which is exactly why 3a says not to open 4000 there. The two layers together give
> you: 80/443 public, everything else closed.

## 4. Install Docker and bring up the backend

From here it's the standard flow. Install Docker with the convenience script (it detects ARM and
does the right thing):

```bash
curl -fsSL https://get.docker.com | sh
sudo usermod -aG docker $USER
newgrp docker
sudo systemctl enable --now docker
```

Then follow your delivery path's guide from "Get the code" onward, right on this VM:

- **[Native APNs guide](GETTING_STARTED.md#2-get-the-code)** (paid Apple Developer account), or
- **[Bark guide](GETTING_STARTED_BARK.md#2-get-the-code)** (free)

…and come back here for the exposure step.

## 5. Expose it with Caddy

Your VM already has a public IP, so this is
[Step 8a of the APNs guide](GETTING_STARTED.md#8a-vps--caddy-reverse-proxy-most-reliable) minus
the "create a VPS" part:

1. Point a DNS **A record** (e.g. `apollo.yourdomain.com`) at the VM's public IP.
2. Install [Caddy](https://caddyserver.com/docs/install) and create `/etc/caddy/Caddyfile`:
   ```
   apollo.yourdomain.com {
       reverse_proxy localhost:4000
   }
   ```
   then `sudo systemctl reload caddy`. Caddy fetches a Let's Encrypt certificate automatically —
   this is what ports 80/443 were opened for.
3. Set the app's **Backend URL** to `https://apollo.yourdomain.com` and make sure
   `REGISTRATION_SECRET` is set (the API is now on the public internet).

No domain? A free [DuckDNS](https://www.duckdns.org/) hostname pointed at the static public IP
works with the same Caddyfile. (Unlike a home connection, the VM's public IP doesn't change, so
you don't need the DDNS updater — set it once.)

## 6. Free-tier housekeeping

- **Guard against idle reclamation** (Always Free accounts only — PAYG is exempt): the backend's
  steady Reddit polling generates real CPU and network activity, but a single-account deployment
  may still sit under Oracle's ~20% thresholds. Check **Compute → Instances → your instance →
  Metrics** after a week; if you're hovering near reclamation territory, the honest fix is the
  PAYG upgrade (catch #3). If Oracle does stop the instance, your data survives — the boot volume
  persists and the instance can be restarted, though repeated reclamation is a sign to upgrade.
- **Set a budget alert** regardless: **Billing → Budgets → Create budget** with a $1 threshold
  emails you if anything ever starts costing money.
- **Reboots are handled** if you followed the guides: Docker is `systemctl enable`d, the compose
  services are `restart: unless-stopped`, and Caddy runs as a systemd service. Oracle does
  occasionally do maintenance reboots on free instances, so this matters more here than at paid
  providers.
- **Backups**: the `pg_dump` snippet from
  [Step 9 of the APNs guide](GETTING_STARTED.md#9-keep-it-running-long-term) applies unchanged.
  Copy the dumps off the VM (`scp`) now and then — the free tier has no automatic volume backups.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| "Out of capacity for shape VM.Standard.A1.Flex" | Free-tier A1 scarcity in your home region | Retry later (capacity frees up continuously), try another availability domain, shrink the shape, or upgrade to PAYG (catch #3) |
| `curl https://your.domain` times out, but everything works on the VM | One of the **two** firewalls is still closed | Re-do [Step 3](#3-open-the-firewalls-both-of-them) — you need *both* the VCN ingress rules **and** the iptables ACCEPT rules |
| Ports open, DNS resolves, but Caddy can't get a certificate | Port 80 blocked (cert challenge needs it), or the A record points somewhere else | Confirm both firewalls pass 80, and `dig apollo.yourdomain.com` returns the VM's public IP |
| Instance stopped by itself | Idle reclamation (Always Free accounts) | Restart it from the console; to stop it recurring, upgrade to PAYG |
| `exec format error` from a container | An image without an `arm64` build (not one of the bundled ones) | Only affects extra images you've added yourself; find an arm64-capable alternative |

Everything else — containers restart-looping, pushes not arriving, Reddit 403s — is backend
behavior, not Oracle behavior: see the troubleshooting sections of the
[APNs](GETTING_STARTED.md#10-troubleshooting) and [Bark](GETTING_STARTED_BARK.md#10-troubleshooting)
guides.
