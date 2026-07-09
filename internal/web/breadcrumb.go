package web

// Crumb is one node in a page's breadcrumb trail (breadcrumb-trail-landing,
// landing breadcrumb-nav-trail's never-merged design). Href is the link target
// for an ANCESTOR crumb; the terminal (current-page) crumb has an empty Href and
// the breadcrumb partial (layout.html) renders it as aria-current text rather
// than a link. Every builder below therefore leaves ONLY the last crumb's Href
// empty, so "empty Href == current page" is an invariant the template can key off
// without a separate flag.
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

// envCrumbs: dashboard > <name> > env — the submodule's blue/green deploy panel
// (/submodule/<name>/env), a child of the submodule explorer like plan/docs, so
// it threads the same crumbSubmodule ancestor. The leaf label mirrors the route
// segment and the dashboard/explorer's lowercase "…deploy env" navigation.
func envCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name), {Label: "env"}}
}

// roiCrumbs: dashboard > <name> > roi — the submodule's intent editor. Its route
// is top-level (/roi/<name>) rather than under /submodule/<name>/, but the ROI is
// a property OF that submodule, so it hangs off the submodule explorer exactly
// like the other scoped pages. The leaf label is the lowercase "roi" the
// dashboard card links it as (the leaf convention mirrors the card's nav labels).
func roiCrumbs(name string) []Crumb {
	return []Crumb{crumbDashboard(), crumbSubmodule(name), {Label: "roi"}}
}

// humanResolveCrumbs: dashboard > human > <sub>/<id> — the per-task resolution
// workspace (/human/<sub>/<id>). Unlike the submodule-scoped pages it hangs off
// the GLOBAL NEEDS-HUMAN queue (/human, the page it is reached from), NOT a
// submodule crumb: the queue spans every submodule, so the leaf names <sub>/<id>
// to disambiguate which blocked task. The "human" ancestor stays a link back to
// the queue — the same drill-down shape sessionCrumbs uses for its listing
// ancestor.
func humanResolveCrumbs(sub, id string) []Crumb {
	return []Crumb{crumbDashboard(),
		{Label: "human", Href: "/human"}, {Label: sub + "/" + id}}
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
