# Tailscale Funnel Setup for the Relay

## Overview

This guide sets up a stable public HTTPS endpoint for the relay using Tailscale Funnel. This provides:

- **Stable hostname** — Push subscriptions persist across sessions (critical for Web Push reliability)
- **HTTPS automatically** — Service workers and Web Push API require secure origins
- **No inbound ports** — The relay remains behind your laptop's firewall; Funnel tunnels outbound

Without a stable hostname, the Android device's Web Push subscription would break on each relay restart, making background notifications unreliable.

## Prerequisites

1. **Tailscale account** — Create one at [tailscale.com](https://tailscale.com)
2. **Tailscale CLI installed**
   - macOS: `brew install tailscale` (then `sudo /Applications/Tailscale.app/Contents/MacOS/Tailscale up`)
   - Linux: `curl -fsSL https://tailscale.com/install.sh | sh`
3. **Funnel enabled in your tailnet** — Ask your tailnet admin to enable the `funnel` nodeAttr, or check your admin console:
   - Go to [Tailscale Admin Console](https://login.tailscale.com/admin)
   - ACLs → Policy File
   - Ensure `nodeAttrs` includes `"funnel"` (e.g., `"nodeAttrs": ["funnel"]`)
4. **Relay built and ready**
   - Run `make build` from the project root
   - Set `OPENAI_API_KEY` environment variable
5. **Project token known** — The relay generates a token on first run (see "Deriving the Companion URL" below)

## Step-by-Step Setup

### 1. Authenticate to Tailscale

```bash
tailscale up
```

This opens a browser to authenticate and add your machine to your tailnet. Confirm the device appears in your [Tailscale Admin Console](https://login.tailscale.com/admin/machines).

### 2. Start the Relay

From the project root:

```bash
OPENAI_API_KEY=<your-key> make run
```

The relay starts and listens on:
- **Public interface:** `0.0.0.0:8766` (this is what Funnel will expose)
- **Ingest server:** `127.0.0.1:8765` (loopback only, not exposed)

You should see output like:
```
[relay] listening on 0.0.0.0:8766
[ingest] listening on 127.0.0.1:8765
```

### 3. Enable Funnel on Port 8766

In a new terminal (the relay keeps running in the first):

```bash
tailscale funnel 8766
```

This exposes the relay's public port through Tailscale Funnel. You should see output like:

```
Available over the internet:
|-- URL: https://<machine>.<tailnet>.ts.net
|-- Warning: exposed on the internet. Control with `tailscale funnel off 8766`
```

**Save this URL** — it is your stable public hostname. It will remain the same across relay restarts.

### 4. Verify the Setup

#### Check Funnel Status
```bash
tailscale funnel status
```

You should see port 8766 listed and active.

#### Test the Public URL
From a different network (or using cellular on your phone), visit:
```
https://<machine>.<tailnet>.ts.net
```

Without a token in the URL, you should get a redirect or 401 error (expected — the relay requires authentication). Proceed to the next step.

#### Verify No Inbound Ports Opened
Run an external port scan or ask your network admin to confirm no new inbound ports are exposed on your laptop:
```bash
# macOS/Linux: check that you haven't opened inbound ports
sudo lsof -i -P -n | grep LISTEN
```

The relay should NOT appear in the inbound listen list (it only binds to `0.0.0.0:8766`, but Tailscale handles the inbound side).

## Deriving the Companion URL

The relay generates an authentication token on startup. This token is embedded in the companion URL as a path prefix.

### Find Your Token

1. **From the relay's startup output**, look for a line like:
   ```
   token=<token-here>
   ```

2. **Or check the token file** (if running locally):
   ```bash
   cat ~/.claude/plugins/claude-code-tts/token
   ```

### Construct the Companion URL

Combine your Funnel hostname and token:

```
https://<machine>.<tailnet>.ts.net/<token>/
```

**Example:**
```
https://mylaptop.example-tailnet.ts.net/abc123def456/
```

This is the URL you will share with the Android device or encode in a QR code.

## Persisting the Funnel Across Sessions

To keep the Funnel active across terminal sessions or reboots:

### Option A: Background Process (Recommended)

Run the relay and Funnel as background services:

```bash
# Terminal 1: Start the relay in the background
OPENAI_API_KEY=<key> nohup make run > relay.log 2>&1 &

# Terminal 2: Start Funnel in the background
nohup tailscale funnel 8766 > funnel.log 2>&1 &
```

Or use `screen` or `tmux` to keep them running.

### Option B: Systemd Service (Linux)

If you want the relay to start automatically on boot, create a systemd service file. (Out of scope for this guide; see your OS's documentation.)

### Verifying Persistence

After any restart, confirm:

```bash
tailscale funnel status
# Should show port 8766 active

# Verify the URL is still reachable
curl https://<machine>.<tailnet>.ts.net/<token>/
```

The key property: **the hostname `<machine>.<tailnet>.ts.net` is stable and does not change** across restarts. This ensures the Android device's Web Push subscription remains valid.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `tailscale funnel 8766` fails with "Funnel not available" | Ensure `funnel` nodeAttr is enabled in your tailnet ACL policy (Admin Console → ACLs → Policy File). |
| `tailscale funnel 8766` succeeds but URL returns 502 | The relay is not running on port 8766. Verify `make run` started successfully and check port with `lsof -i :8766`. |
| Certificate not yet provisioned (HTTPS error on first access) | Tailscale provisions the certificate on first access. Wait 10–30 seconds and try again. |
| Companion loads but push subscription fails | Ensure: (1) you're visiting `https://` (not `http://`), (2) notification permission is granted, (3) the relay's `/push/vapid-public-key` endpoint is reachable. |
| Port 8766 already in use | Change the port (edit `cmd/relay/main.go` and rebuild) or kill the process using it. |
| "Address already in use" after reboot | Old relay process still running. Kill it: `pkill -f "make run"` or `pkill relay`. |

## Security Notes

### Loopback-Only Ingest
The ingest server (port 8765) remains on `127.0.0.1` and is never exposed through Funnel. Only the public relay (port 8766) is accessible over the internet. This prevents untrusted sources from directly injecting clips.

### Token as Authentication
The token is the sole form of authentication and is embedded in every request URL. Treat it like a password:
- Do not share it in logs or error messages.
- Rotate it if you suspect compromise (regenerate on relay restart).

### Firewall Unchanged
Funnel tunnels connections outbound through Tailscale's network. Your laptop's firewall settings do not need to change. Run `tailscale funnel off 8766` to immediately disable public access without any firewall cleanup.

## Tearing Down the Funnel

To stop exposing the relay:

```bash
tailscale funnel off 8766
```

The relay continues running locally. To fully stop everything:

```bash
tailscale down
pkill relay
```

## Next Steps

With the Funnel URL stable and verified, proceed to the [Android Verification Checklist](android-verification-checklist.md) to test end-to-end push and auto-play scenarios on a real device.
