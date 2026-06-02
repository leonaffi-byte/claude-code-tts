/* Service worker for TTS Companion — handles Web Push notifications and
   tap-to-play forwarding to the companion page. */

self.addEventListener('push', function (event) {
  var data = {};
  if (event.data) {
    try {
      data = event.data.json();
    } catch (e) {
      data = { clipId: 'unknown', clipURL: '' };
    }
  }

  var clipId = data.clipId || 'unknown';
  var clipURL = data.clipURL || '';

  var title = 'TTS Clip Ready';
  var options = {
    body: 'Tap to play clip ' + clipId,
    tag: 'tts-clip-' + clipId,
    renotify: true,
    data: { clipId: clipId, clipURL: clipURL },
    actions: [{ action: 'play', title: 'Play' }]
  };

  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', function (event) {
  event.notification.close();

  var notifData = event.notification.data || {};
  var clipId = notifData.clipId || '';
  var clipURL = notifData.clipURL || '';

  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function (windowClients) {
      // Post to any already-open companion window so it plays immediately.
      for (var i = 0; i < windowClients.length; i++) {
        windowClients[i].postMessage({ type: 'play-clip', clipId: clipId, clipURL: clipURL });
        return windowClients[i].focus();
      }
      // No open window — open companion with the clip encoded in the URL so
      // the page can start playing immediately on load without needing a
      // separate postMessage handoff.
      var query = '?autoplay=' + encodeURIComponent(clipId);
      if (clipURL) { query += '&clipURL=' + encodeURIComponent(clipURL); }
      return clients.openWindow('./' + query);
    })
  );
});
