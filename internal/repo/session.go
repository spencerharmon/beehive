package repo

import "strings"

// Session transcripts stream to an isolated per-session git branch (rapid
// commits, pushed to a remote when one exists) instead of being merged into the
// shared main branch on every poll — that raced and truncated transcripts under
// concurrency, and assumed beehived shared a filesystem with the honeybee. While
// a session runs, main carries only a STUB at sessions/<sid>.md naming the live
// branch; beehived reads the stub, resolves the branch (fetching from the remote
// when distributed), and renders the branch's copy of the same path. At session
// end the branch is squashed and merged once, so main holds the durable final
// transcript. The stub is the branch's first commit, so that final merge never
// conflicts on the file.

const sessionStubPrefix = "beehive-session-branch:"

// SessionStub is the placeholder committed to main at a session's path while the
// transcript streams to SessionBranch. It is a single HTML comment line so it
// renders invisibly if shown raw, plus a human note.
func SessionStub(branch string) string {
	return "<!-- " + sessionStubPrefix + " " + branch + " -->\n\n" +
		"_Session is streaming live on branch `" + branch + "`; the final transcript lands here when it ends._\n"
}

// ParseSessionStub returns the branch a stub points to. ok is false when the
// content is a real transcript (a finished session), so the caller renders it
// directly instead of resolving a branch.
func ParseSessionStub(content string) (branch string, ok bool) {
	// The marker is the first line. Leading BOM/whitespace is tolerated, but a
	// real transcript that merely mentions the marker in its body is not a stub.
	head := strings.TrimLeft(strings.TrimPrefix(content, "\ufeff"), " \t\r\n")
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	head = strings.TrimSpace(head)
	const marker = "<!-- " + sessionStubPrefix
	if !strings.HasPrefix(head, marker) {
		return "", false
	}
	rest := strings.TrimLeft(head[len(marker):], " \t") // skip the space after the colon
	if j := strings.IndexAny(rest, " \t>"); j >= 0 {
		// stop at whitespace or the closing of the HTML comment
		rest = rest[:j]
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}
