package web

// Crumb is one node in a page's breadcrumb trail (breadcrumb-nav-trail). Href is
// the link target for an ANCESTOR crumb; the terminal (current-page) crumb has an
// empty Href and the breadcrumb partial (layout.html) renders it as aria-current
// text rather than a link. Every builder below therefore leaves ONLY the last
// crumb's Href empty, so "empty Href == current page" is an invariant the
// template can key off without a separate flag.
type Crumb struct {
	Label string
	Href  string
}

// crumbDashboard is the root crumb every non-dashboard page hangs off. The
// dashboard itself renders no trail — it is the root.
func crumbDashboard() Crumb { return Crumb{Label: "dashboard", Href: "/"} }

// crumbSubmodule links to a submodule's explorer page (/submodule/<name>), the
// common parent of that submodule's plan/sessions/docs/branches views.
func crumbSubmodule(name string) Crumb { return Crumb{Label: name, Href: "/submodule/" + name} }

// explorerCrumbs: dashboard > <name> (the submodule page is the current page, so
// its crumb carries no link).
func explorerCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), {Label: name}}
}

// planCrumbs: dashboard > <name> > plan.
func planCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name), {Label: "plan"}}
}

// sessionsCrumbs: dashboard > <name> > sessions.
func sessionsCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name), {Label: "sessions"}}
}

// sessionCrumbs: dashboard > <name> > sessions > <branch> — the deep session
// view, whose "sessions" ancestor stays a link back to the listing.
func sessionCrumbs(name, branch string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name),
		{Label: "sessions", Href: "/submodule/" + name + "/sessions"}, {Label: branch}}
}

// docsCrumbs: dashboard > <name> > docs (the whole-tree doc explorer).
func docsCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name), {Label: "docs"}}
}

// branchesCrumbs: dashboard > <name> > commits (the branch/commit graph, which
// titles itself "<name> commits").
func branchesCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name), {Label: "commits"}}
}

// commitCrumbs: dashboard > <name> > commits > <sha> — the deep commit view,
// whose "commits" ancestor stays a link back to the branch graph.
func commitCrumbs(name, sha string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name),
		{Label: "commits", Href: "/submodule/" + name + "/branches"}, {Label: sha}}
}

// docCrumbs builds the doc viewer's trail, whose intermediate crumb reflects the
// page the reader ACTUALLY came from (the `from` query token threaded by every
// caller) rather than the old hardcoded "<name> commits" back-link — which was
// wrong whenever the doc was reached from the plan's change-doc column, the doc
// explorer's listing, or a /stats delivery row. Known tokens map to their entry
// page; an unknown or empty token defaults to the submodule page (dashboard >
// <name> > <file>), a sane parent when the entry route is unknown. `stats` is the
// one GLOBAL entry (dashboard > stats > <file>); the rest are scoped under this
// submodule.
func docCrumbs(name, from, file string) []Crumb {
	root, sub, leaf := crumbDashboard(), crumbSubmodule(name), Crumb{Label: file}
	switch from {
	case "plan":
		return []Crumb{root, sub, {Label: "plan", Href: "/submodule/" + name + "/plan"}, leaf}
	case "docs":
		return []Crumb{root, sub, {Label: "docs", Href: "/submodule/" + name + "/docs"}, leaf}
	case "branches":
		return []Crumb{root, sub, {Label: "commits", Href: "/submodule/" + name + "/branches"}, leaf}
	case "stats":
		return []Crumb{root, {Label: "stats", Href: "/stats"}, leaf}
	default:
		return []Crumb{root, sub, leaf}
	}
}
