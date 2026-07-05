package repo

// RootInstructionFile is one repo-ROOT instruction file surfaced UNIFORMLY by the
// frontend whether or not it currently exists on disk. The set is DECLARED (see
// RootInstructionFiles), never derived from a directory listing, so an absent
// member — a managed default that was removed, or a LOCALS.md that was never
// authored — stays discoverable and offers a create flow instead of silently
// vanishing.
//
// The AGENTS.md member is the ROOT generic operating guide, NOT a per-submodule
// submodules/<name>/AGENTS.md overlay: the two are different files with different
// ownership (the root guide is a beehive-managed default; a submodule overlay is
// per-target content) and must not be conflated.
type RootInstructionFile struct {
	// Name is the repo-root-relative path — a bare basename for every current
	// member. It is both the presence stat target and the chat-diff editor
	// ?path= value, so opening an absent file's link starts a create on an empty
	// base while a present file's link opens view+edit on the real content.
	Name string
	// Title is a short human label for the UI.
	Title string
	// Purpose is a one-line description of the file's role, shown in the UI and
	// summarizing what a create flow for an absent member should produce.
	Purpose string
	// Managed marks a beehive-shipped instruction default that `beehive
	// instruction update` owns and refreshes (AGENTS.md, HONEYBEE.md,
	// BOOTSTRAP.md). LOCALS.md is site-authored (Managed=false): it is NEVER
	// beehive-managed and must never be auto-generated or overwritten by an
	// update. instruction-update-drift keys its refresh/drift check on this flag.
	Managed bool
}

// RootInstructionFiles returns the declared set of repo-root instruction files in
// display order. It is intentionally a FIXED set rather than an os.ReadDir of the
// root: the frontend renders every member uniformly — including any that are
// currently absent — so a missing managed default or an unwritten LOCALS.md is
// visible and creatable. Managed marks the three beehive-shipped defaults; the
// site-authored LOCALS.md is the sole unmanaged member.
func RootInstructionFiles() []RootInstructionFile {
	return []RootInstructionFile{
		{
			Name:    AgentsFile,
			Title:   AgentsFile,
			Purpose: "generic operating guide for any agent working this beehive repo (the root guide, not a submodule overlay)",
			Managed: true,
		},
		{
			Name:    HoneybeeFile,
			Title:   HoneybeeFile,
			Purpose: "the honeybee runtime protocol the runner injects into every pass",
			Managed: true,
		},
		{
			Name:    BootstrapFile,
			Title:   BootstrapFile,
			Purpose: "step-by-step walkthrough to stand up a new install",
			Managed: true,
		},
		{
			Name:    LocalsFile,
			Title:   LocalsFile,
			Purpose: "site-specific operator facts (paths, build/deploy, scheduler, hosts, safety); authored per install, never beehive-managed",
			Managed: false,
		},
	}
}
