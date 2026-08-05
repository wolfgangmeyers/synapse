---
name: remote-cloud-desktop
description: Provision a generic Ubuntu cloud desktop using KDE Plasma X11, xrdp, and private-overlay-only RDP access. Use when setting up or validating a remote Linux desktop for Mac/Windows RDP clients without exposing RDP publicly.
---

# Remote Cloud Desktop

Set up an Ubuntu cloud VM as a KDE Plasma desktop reachable by RDP only through a private overlay network.

This skill is intentionally generic. Do not bake in environment-specific addresses, provider account details, local paths, deployment names, or credentials.

## Contract

Smallest outcome: an Ubuntu user can connect with an RDP client to a KDE Plasma X11 session over a private network path.

Non-goals:

- No VNC setup unless RDP is impossible.
- No full desktop meta-package unless the minimal KDE packages are unavailable.
- No application dependencies beyond the desktop, xrdp, Xorg backend, and private-overlay client packages.
- No credential generation, credential disclosure, or password selection by the agent.
- No cloud-provider-specific automation unless the user explicitly asks for that provider.

## Evidence boundary

The reusable path below comes from two evidence classes:

- Verified current state: Ubuntu with `kde-plasma-desktop`, `plasma-session-x11`, `kwin-x11`, `xrdp`, `xorgxrdp`, `ssl-cert`, active `xrdp`/`xrdp-sesman`, and `.xsession` containing `exec /usr/bin/startplasma-x11`.
- Historical setup evidence: an earlier XFCE RDP setup was replaced with KDE Plasma X11. XFCE is not part of the final target state.

Keep any host-specific evidence in private validation notes. The published instructions stay environment-neutral.

## Preconditions

- Ubuntu cloud VM with SSH access and sudo.
- A normal login user for the desktop session.
- A private overlay network client that assigns a stable private interface or private address on the VM.
- Cloud firewall/security-group control that can keep public inbound TCP `3389` closed.
- A human-controlled password decision for the desktop user. xrdp authenticates with the Linux account password; do not invent or transmit it.

## Provision KDE Plasma over xrdp

Run as the desktop login user unless a command uses `sudo`.

```bash
sudo apt-get update
sudo apt-get install --no-install-recommends \
  kde-plasma-desktop \
  plasma-session-x11 \
  kwin-x11 \
  xrdp \
  xorgxrdp \
  ssl-cert
```

Allow xrdp to read the distro-provided TLS key material:

```bash
sudo adduser xrdp ssl-cert
```

Configure the login user's xrdp session to start KDE Plasma on X11:

```bash
printf '%s\n' 'exec /usr/bin/startplasma-x11' > "$HOME/.xsession"
chmod 600 "$HOME/.xsession"
```

Enable and start xrdp:

```bash
sudo systemctl enable --now xrdp xrdp-sesman
sudo systemctl restart xrdp-sesman xrdp
```

## Private-overlay-only access

xrdp normally listens on TCP 3389. Do not expose that port to the public Internet.

Keep these boundaries separate:

1. Cloud firewall/security group: public inbound TCP `3389` must be closed. Do not allow RDP by trying to source-allowlist private overlay addresses here; the cloud firewall sees the overlay transport's public source, not the inner private overlay client address.
2. Private overlay transport: install/enroll the overlay client through its official Ubuntu package path. Keep auth keys out of logs and docs. A human or secure secret manager should provide any enrollment token. Do not document or alter the overlay transport ports unless the user explicitly asks for that overlay product.
3. Host RDP listener: bind xrdp to the VM's private overlay IP, or otherwise enforce TCP `3389` only on the private overlay interface at the host. This host-local control is the RDP access boundary.

To bind xrdp to the overlay IP, first identify the VM's private overlay address:

```bash
ip -br addr
```

Then set the global xrdp listener to that address. Replace the placeholder with the VM's private overlay IP, not a public address:

```bash
sudo cp /etc/xrdp/xrdp.ini /etc/xrdp/xrdp.ini.bak
sudo python3 - <<'PY'
from pathlib import Path

overlay_ip = '<private-overlay-ip>'
path = Path('/etc/xrdp/xrdp.ini')
lines = path.read_text().splitlines()
for i, line in enumerate(lines):
    if line.startswith('port='):
        lines[i] = f'port=tcp://{overlay_ip}:3389'
        break
else:
    raise SystemExit('No global port= line found in /etc/xrdp/xrdp.ini')
path.write_text('\n'.join(lines) + '\n')
PY
sudo systemctl restart xrdp
```

If the host firewall is enabled, keep it consistent with the xrdp bind: allow TCP `3389` only on the private overlay interface/IP and deny it on public interfaces.

Do not rely on cloud source allowlists for overlay client identity, and do not rely on `xrdp.ini` alone if the host has other listeners or forwarding rules. Treat the combination of public TCP `3389` closed plus host-local overlay-only RDP as the access boundary.

## User password boundary

If the login account has no usable password, stop and ask the human to set one over SSH:

```bash
sudo passwd "$USER"
```

Do not choose, store, print, or send the password.

## Validation

Package state:

```bash
dpkg-query -W \
  kde-plasma-desktop \
  plasma-session-x11 \
  kwin-x11 \
  xrdp \
  xorgxrdp \
  ssl-cert
```

Session startup:

```bash
test "$(cat "$HOME/.xsession")" = 'exec /usr/bin/startplasma-x11'
stat -c '%U %G %a %n' "$HOME/.xsession"
```

xrdp service state:

```bash
systemctl is-enabled xrdp xrdp-sesman
systemctl is-active xrdp xrdp-sesman
id xrdp
ss -ltnp | grep ':3389'
# The listener should show the private overlay IP, not 0.0.0.0, ::, or *.
```

Network reachability:

```bash
# From a private overlay client to the VM's private overlay address: should succeed.
nc -vz <private-overlay-address-or-name> 3389

# From outside the private overlay network to the VM's public address: should fail or time out.
nc -vz <public-address-or-name> 3389
```

RDP client check:

- Use a standard RDP client for your workstation OS.
- Connect to the private overlay address or private overlay DNS name on port `3389`.
- Log in as the Ubuntu desktop user with the human-chosen Linux password.
- Accept the expected first-use certificate warning if using the package default certificate.

## Troubleshooting

- Black screen or immediate disconnect: confirm `.xsession` is exactly `exec /usr/bin/startplasma-x11`, `plasma-session-x11` and `kwin-x11` are installed, then restart `xrdp-sesman` and `xrdp`.
- Login fails: verify the Linux account has a password and is not locked. Do not set the password yourself unless the human explicitly provided that decision.
- Public RDP reachable: close public inbound TCP `3389` in the cloud firewall/security group, then confirm xrdp is bound to the private overlay IP or blocked on public interfaces at the host.
- Wayland sessions: xrdp uses Xorg here. Keep the KDE session on `startplasma-x11`.
- VNC temptation: do not install VNC if xrdp is healthy and listening. RDP is the target path.
