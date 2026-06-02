package relay

// PresenceTracker reports whether at least one foreground SSE listener is
// currently connected to the relay. Handler uses this to decide whether to
// suppress a Web Push notification after broadcasting a new clip via SSE.
//
// When HasLiveListener returns true, the clip already auto-plays in the
// companion; sending a push would cause double delivery. When it returns
// false, no companion is watching and a push is the only delivery channel.
type PresenceTracker interface {
	// HasLiveListener returns true if at least one SSE client is currently
	// subscribed and receiving events. It must be safe for concurrent use.
	HasLiveListener() bool
}
