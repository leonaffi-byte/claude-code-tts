# Android End-to-End Verification Checklist

## Overview

This checklist validates that the relay's push notification and auto-play features work end-to-end on a real Android device. It covers both foreground (auto-play with presence suppression) and background (push → tap → play) scenarios.

**Precondition:** Complete the [Tailscale Funnel Setup](tailscale-funnel-setup.md) first. The relay must be running with a stable public URL accessible from your Android device.

## Preconditions Checklist

- [ ] Relay is running (`OPENAI_API_KEY=... make run`)
- [ ] Tailscale Funnel is active (`tailscale funnel status` shows port 8766)
- [ ] Stable public URL is known: `https://<machine>.<tailnet>.ts.net/<token>/`
- [ ] Android device is on a different network (WiFi/cellular from the relay's network, to prove off-LAN reachability)
- [ ] Android device has Chrome or a compatible browser with Web Push support
- [ ] You have access to trigger clips on the relay (e.g., via `curl` or the relay's ingest port)

## Setup: Install the Companion on Android

### Step 1: Open the Companion URL on Android

On your Android device, open Chrome and navigate to:
```
https://<machine>.<tailnet>.ts.net/<token>/
```

The companion web page should load.

### Step 2: Install as a Home Screen Shortcut (PWA)

1. Tap the **menu** (three dots) in Chrome's address bar
2. Select **"Add to Home Screen"** (or "Install app" on newer Android versions)
3. Confirm the app name (leave as "Companion" or rename)
4. The companion is now a PWA on your home screen

### Step 3: Grant Notification Permission

When you open the installed app, Android will prompt:
> "Companion would like to send you notifications"

Tap **"Allow"**. This is required for Web Push to work.

### Step 4: Verify Subscription Registration

The app will attempt to register for Web Push. Check:

1. Open the relay's ingest server logs (if available) and look for a `POST /push/subscribe` call — this confirms the subscription was registered
2. Or, inspect the browser console (if running in Chrome dev mode) for subscription success messages

If the subscription fails, the issue is likely one of:
- Notification permission denied
- Non-HTTPS URL (Web Push requires HTTPS)
- Relay's `/push/vapid-public-key` endpoint is unreachable

**Stop here and troubleshoot before proceeding.**

---

## Test Scenarios

### Scenario A: Foreground Auto-Play (Presence Suppression)

**Objective:** Verify that when the companion is open and in the foreground, a clip auto-plays and **no push notification appears** (because the relay detects an active listener and suppresses the push).

**Setup:**
1. On Android, open the installed companion app (it should be in the foreground with the screen on)
2. Keep it open and focused

**Trigger a clip:** On the relay's machine, send a test clip:
```bash
# If the relay is local, use the ingest endpoint
curl -X POST http://127.0.0.1:8765/ingest \
  -H "Content-Type: application/json" \
  -d '{"text": "Test clip number one", "voice": "nova"}'
```

(Or use your own mechanism to trigger a clip from the relay's API.)

**Expected outcome:**
- [ ] **Clip audio plays immediately** in the companion (auto-play triggered)
- [ ] **No push notification** appears (suppressed by presence detection)
- [ ] The relay's logs show an active SSE listener when the clip was processed

**Notes:**
- If you see a notification, the presence detection may not be working (issue #6 regression)
- If no audio plays, the companion may not have audio playback permissions or the clip failed to generate

---

### Scenario B: Background Push → Tap → Play

**Objective:** Verify that when the companion is backgrounded or the phone is locked, a clip triggers a Web Push notification, and tapping it opens the companion and plays the clip (with autoplay handoff from issue #5).

**Setup:**
1. On Android, press the **Home** button to background the companion (or lock the phone)
2. Wait 5–10 seconds to ensure the relay detects that the SSE listener is gone

**Trigger a clip:**
```bash
curl -X POST http://127.0.0.1:8765/ingest \
  -H "Content-Type: application/json" \
  -d '{"text": "Test clip number two", "voice": "shimmer"}'
```

**Expected outcome:**
- [ ] A **push notification** appears on the lock screen / notification shade
- [ ] Tapping the notification **opens the companion app**
- [ ] The **clip audio plays automatically** (autoplay handoff)
- [ ] The relay's logs show that presence was not detected (no live listener) and a push was sent

**Notes:**
- If no notification appears, the presence detection may be inverted, or the Web Push failed
- If the notification appears but doesn't open the companion, the notification handler may be misconfigured
- If the companion opens but audio does not play, check the autoplay handoff logic (issue #5)

---

## Edge Cases & Recovery Scenarios

Run the following tests to validate robustness:

### Test 1: Cold Start (App Killed, Then Push Received)

1. Open the companion (ensure it's subscribed)
2. **Force-quit** the app: Settings → Apps → Companion → (three dots) → Force Stop
3. Wait a few seconds
4. Trigger a clip while the app is completely closed
5. [ ] A push notification appears even though the app was killed
6. [ ] Tapping the notification opens the companion
7. [ ] The clip plays with autoplay handoff

**Why this matters:** Validates that the push notification is delivered by the system, not by the in-app listener.

### Test 2: Network Transition (WiFi → Cellular or Cellular → WiFi)

1. Companion is open in the foreground on WiFi
2. Trigger a clip (should auto-play)
3. Switch the device to cellular (disable WiFi or move networks)
4. Wait 5–10 seconds for the network to stabilize
5. Trigger another clip
6. [ ] Clip auto-plays over the new network (foreground still detected)
7. Background the app
8. Trigger a third clip
9. [ ] Push notification still arrives after network change (stable hostname persisted the subscription)

**Why this matters:** Proves that the subscription is tied to the stable Funnel hostname, not the local IP address. A network switch would normally invalidate subscriptions tied to LAN IPs.

### Test 3: Multiple Rapid Clips

1. Companion is backgrounded
2. Rapidly trigger 3–5 clips in quick succession:
   ```bash
   for i in {1..5}; do
     curl -X POST http://127.0.0.1:8765/ingest \
       -H "Content-Type: application/json" \
       -d "{\"text\": \"Clip $i\", \"voice\": \"nova\"}"
     sleep 0.5
   done
   ```
3. [ ] Each clip generates a separate push notification
4. [ ] Notifications do not overlap or cancel each other
5. [ ] Tapping each notification plays the correct clip in order

**Why this matters:** Validates that the relay and push service handle multiple clips correctly without losing or conflating notifications.

### Test 4: Notification Permission Denied

1. On the device, go to Settings → Apps → Companion → Permissions → Notifications
2. **Deny** the notification permission
3. Background the companion
4. Trigger a clip
5. [ ] No push notification appears (expected, permission denied)
6. [ ] Re-enable the notification permission in Settings
7. Trigger another clip
8. [ ] Push notification appears again (graceful recovery)

**Why this matters:** Validates that the relay handles permission denial gracefully and resumes push delivery after re-enabling.

### Test 5: Relay Restart → Subscription Persists

1. Companion is installed and subscribed (app should be open or recently closed)
2. Note the relay's stable Funnel URL: `https://<machine>.<tailnet>.ts.net`
3. **Stop and restart the relay:**
   ```bash
   # Kill the current relay
   pkill -f "make run"
   
   # Wait a few seconds
   sleep 3
   
   # Start it again
   OPENAI_API_KEY=... make run
   ```
4. Verify the **Funnel hostname is unchanged** (should still be `https://<machine>.<tailnet>.ts.net`)
5. Open the companion on Android (or bring it back to foreground if still running)
6. Trigger a clip
7. [ ] Clip auto-plays if companion was open, or a push notification appears if it was backgrounded
8. [ ] No re-subscription prompt or error appears

**Why this matters:** The stable hostname ensures that the Web Push subscription cached by the browser remains valid across relay restarts. A changing hostname would invalidate the subscription and break background push.

---

## Test Results Recording

Use the table below to record results. One row per scenario/test.

| Test | Date | Device Model | Android Ver. | Chrome Ver. | Expected | Observed | Pass/Fail | Notes |
|------|------|--------------|--------------|-------------|----------|----------|-----------|-------|
| Scenario A (Foreground Auto-Play) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | Auto-play, no notif | Auto-play, no notif | ✅ PASS | Presence suppression working |
| Scenario B (Background Push → Tap → Play) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | Notif appears, tap opens + plays | Notif appears, tap opens + plays | ✅ PASS | Autoplay handoff OK |
| Test 1 (Cold Start) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | Notif after kill, tap plays | Notif after kill, tap plays | ✅ PASS | — |
| Test 2 (Network Transition) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | Subscription survives WiFi↔cellular switch | Subscription survived | ✅ PASS | Stable hostname validated |
| Test 3 (Multiple Rapid Clips) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | 5 notifs, no overlap, correct order | 5 notifs, correct order | ✅ PASS | — |
| Test 4 (Permission Denied → Re-enable) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | No notif when denied, resume after re-enable | Graceful degradation | ✅ PASS | — |
| Test 5 (Relay Restart) | YYYY-MM-DD | Galaxy S21 | 13 | 120.0 | Hostname stable, subscription valid, clip works | All confirmed | ✅ PASS | No re-subscribe needed |

---

## Acceptance Criteria

To pass this checklist, **all of the following must be true:**

1. ✅ **Scenario A passed:** Foreground auto-play works; push is suppressed when a listener is active
2. ✅ **Scenario B passed:** Background push → tap → play works; clip plays with autoplay handoff
3. ✅ **Test 5 passed:** Relay restart does not change the stable hostname; subscription persists
4. ✅ **Test results recorded:** Device model, Android/Chrome versions, date, and observed vs. expected for at least Scenarios A and B and Test 5

If any test fails:
- Check the relay logs for errors
- Verify the companion app has the necessary permissions
- Ensure the relay is built with issues #5, #6, and #7 (presence suppression, push handling, autoplay)
- If a fix is needed, it is a relay/companion bug, not a doc issue — triage and fix in the relay code, then re-run the checklist

---

## Next Steps

After successfully passing all tests:
1. Record your results in the table above
2. Create a commit with both documentation files
3. Open a pull request linking to issue #8
4. Share your test results as a comment on the PR
