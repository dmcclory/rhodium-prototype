package rhodium

import (
	"rhodium/internal/brain"
	"rhodium/internal/gh"
	"rhodium/internal/tui/router"
)

// cache holds data fetched from GitHub, denormalized for the views to read.
// Populated by async tea.Cmds (loadRepoPRsCmd, loadFilesCmd,
// loadCommentsCmd). PR-scoped maps are keyed by brain.PRKey(repo, n).
// Contributors are cached on the diff view itself, not here, since only
// that view consumes them.
type cache struct {
	allPRs     []gh.PR
	freshKeys  map[string]bool            // confirmed by latest repo listing
	prFiles    map[string][]gh.FileChange // pr key → file list
	prComments map[string][]gh.Comment    // pr key → comments

	// commentsLastSeenAt records the PR's GitHub updatedAt at the time
	// comments were last fetched. Used by the remote-refresh tick to skip
	// comment re-fetches for PRs that haven't had new activity.
	commentsLastSeenAt map[string]string // pr key → updatedAt timestamp
}

func newCache() cache {
	return cache{
		freshKeys:          map[string]bool{},
		prFiles:            map[string][]gh.FileChange{},
		prComments:         map[string][]gh.Comment{},
		commentsLastSeenAt: map[string]string{},
	}
}

// markFresh records that key was present in the most recent repo listing.
// Used by pruneStale to drop PRs that have closed/merged out from under us.
func (c *cache) markFresh(key string) { c.freshKeys[key] = true }

// pruneStale drops allPRs entries that weren't confirmed by the latest
// listing pass. freshKeys is left intact: today this only runs once at
// startup, and onMergeSubmitted maintains the set incrementally after.
func (c *cache) pruneStale() {
	if len(c.freshKeys) == 0 {
		return
	}
	var live []gh.PR
	for _, p := range c.allPRs {
		if c.freshKeys[brain.PRKey(p.Repo, p.Number)] {
			live = append(live, p)
		}
	}
	c.allPRs = live
}

// dropPR removes a PR from allPRs and the per-PR file cache. Used after a
// merge so the PR disappears from lists without waiting for the next
// refetch. prComments is left alone — the entry is unreachable once the
// PR is gone from allPRs.
func (c *cache) dropPR(key string) {
	kept := c.allPRs[:0]
	for _, p := range c.allPRs {
		if brain.PRKey(p.Repo, p.Number) != key {
			kept = append(kept, p)
		}
	}
	c.allPRs = kept
	delete(c.freshKeys, key)
	delete(c.prFiles, key)
	delete(c.prComments, key)
	delete(c.commentsLastSeenAt, key)
}

// session is the user's current navigation state — what PR they're on,
// what file, which review session is active, where to return when they
// back out. Not persisted; restart starts empty. pinnedAttention lives
// here because it's a session-lifetime UI affordance, not GitHub data.
type session struct {
	selectedPR      *gh.PR
	selectedFile    string
	review          *brain.ReviewSession
	listOrigin      router.Route // RouteTodo or RoutePRs — where to return from files
	pinnedAttention map[string]bool
}

func newSession() session {
	return session{
		pinnedAttention: map[string]bool{},
	}
}

// pinAttention marks a PR as pinned in todo's "needs attention" group.
func (s *session) pinAttention(key string) { s.pinnedAttention[key] = true }

// isPinnedAttention reports whether the PR has been pinned this session.
func (s *session) isPinnedAttention(key string) bool { return s.pinnedAttention[key] }

// layout holds terminal viewport state and which view currently has focus.
type layout struct {
	width, height int
	activeView    router.Route
}

// setSize records a new terminal size. Callers are responsible for
// triggering any per-view resize that depends on it.
func (l *layout) setSize(w, h int) { l.width, l.height = w, h }

// focus switches the active view.
func (l *layout) focus(r router.Route) { l.activeView = r }

// status carries transient UI feedback and the polling generation counter.
// pollGen lives here because it's effectively part of the same "things
// the footer / loop care about" surface and has no better home.
type status struct {
	msg     string
	pollGen int
}

// bumpPoll increments and returns the new generation. The polling tick
// carries the generation it was scheduled under, so older loops stop
// naturally when a newer PR is selected.
func (s *status) bumpPoll() int {
	s.pollGen++
	return s.pollGen
}
