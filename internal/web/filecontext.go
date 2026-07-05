package web

import (
	"path"

	"github.com/spencerharmon/beehive/internal/repo"
)

// chat-diff-file-context: per-file editing rules injected into a chat-edit
// session's system prompt so an edit stays correctly formatted and
// protocol-safe. The generic chat-diff surface (chat-diff-editor-core) edits ANY
// repo path, but ROI.md, PLAN.md, RULES.md, and the typed artifact files each
// carry strict formats/ownership rules; without the right preamble the agent
// would happily break the PLAN.md line format or propose an edit to the
// human-owned ROI.md. Both the per-file edit links and the generic edit window
// open through chatManager.open (-> chatSystemPrompt -> the opencode session's
// system seed), so seeding the resolved context there applies the SAME rules to
// a given target no matter how the session was started.
//
// The mapping is data-driven: a table matched by basename, not a switch
// hardcoded per call site. A basename key means the ROI.md rule applies to
// submodules/<sm>/ROI.md and a root ROI.md alike; an unmatched path falls to the
// generic default (an ordinary repo file).

// fileContextRule maps a class of edit targets (identified by basename) to the
// preamble describing how to edit that file safely.
type fileContextRule struct {
	base     string
	preamble string
}

// fileContextRules is the resolver table. Order is irrelevant (basenames are
// unique); the first exact-basename match wins. RULES.md is the beehive-owned
// per-submodule overlay (repo.RulesFile, submodule-rules-md); its rule keys off
// the same constant every other reader uses so the context stays in lockstep.
var fileContextRules = []fileContextRule{
	{repo.ROIFile, roiFileContext},
	{repo.PlanFile, planFileContext},
	{repo.RulesFile, rulesFileContext},
	{repo.AgentsFile, agentsFileContext},
	{repo.InfraFile, infraFileContext},
	{repo.Artifacts, artifactsFileContext},
}

// resolveFileContext returns the editing preamble for a repo-relative path,
// matched by basename against the rule table, or the generic default when no
// rule matches. It is total: every path resolves to some non-empty preamble.
func resolveFileContext(repoPath string) string {
	base := path.Base(path.Clean("/" + repoPath))
	for _, r := range fileContextRules {
		if r.base == base {
			return r.preamble
		}
	}
	return defaultFileContext
}

// The preambles below are intentionally distinct per file class: the acceptance
// contract is that ROI.md, PLAN.md, and an ordinary file yield DIFFERENT,
// file-appropriate rules, and that the seeded session prompt for a path contains
// its rules.

const roiFileContext = `This file is ROI.md — the human-owned record of INTENT for a beehive target. It
states goals, priorities, constraints, and conventions; it is NOT a task list,
status board, or protocol description. Autonomous honeybee agents are FORBIDDEN
to edit ROI.md (a git hook rejects any ROI.md write made under the honeybee
identity) — it is changed only deliberately, by a human operator, through this
editor. Preserve its role: keep it a clear statement of desired outcomes and
conventions, in the operator's own words. Do NOT invent implementation tasks,
status metadata, weights, or machine markers here — those belong in PLAN.md.`

const planFileContext = `This file is PLAN.md — the honeybee-owned task list derived from ROI.md. It is
parsed by a strict, line-oriented format (internal/plan); preserve that format
EXACTLY or the plan will fail to parse:
- The first line is the ROI reconcile stamp comment: <!-- Beehive-ROI: <sha> -->.
- Each task is a level-2 header on ONE line:
  ## <id> [<STATUS>] <!-- attempts=N deps=a,b weight=W session=<id> heartbeat=<RFC3339> -->
  The lines after a header, up to the next header, are that task's free-form body.
- STATUS must be one of: TODO, NEEDS-REVIEW, NEEDS-ARBITRATION, DONE, NEEDS-HUMAN.
  The state machine is TODO -> NEEDS-REVIEW -> {DONE | NEEDS-ARBITRATION};
  NEEDS-ARBITRATION -> {TODO | DONE}; NEEDS-HUMAN is terminal. Never invent a status.
Keep each task header (with its <!-- ... --> metadata comment) intact on a single
line. Do not renumber, reorder, or reflow existing tasks; make the smallest edit
that satisfies the request and leave claim metadata (session/heartbeat) untouched.`

const rulesFileContext = `This file is RULES.md — a per-submodule, beehive-owned rules overlay for agents
working this target. It is ADDITIVE to any AGENTS.md (both are read into agent
context; AGENTS.md is applied first, then RULES.md). Keep it a concise, imperative
list of the LOCAL rules and constraints; do not restate AGENTS.md — state only the
additional rules that apply here.`

const agentsFileContext = `This file is AGENTS.md — an operating guide / rules overlay for agents. Keep it a
clear, imperative set of instructions and conventions. Preserve the existing rules
unless the request is explicitly to change them, and make the smallest coherent
edit; agents read this verbatim as guidance.`

const infraFileContext = `This file is INFRASTRUCTURE.md — a structured document parsed by a typed model
(internal/artifacts) that round-trips its body verbatim plus its Active/Envs deploy
markers (e.g. the blue/green active-env line). Preserve the existing headings and
marker lines exactly; edit prose and values in place rather than restructuring the
document, so the typed parse and the derived deploy state keep working.`

const artifactsFileContext = `This file is ARTIFACTS.md — a structured document parsed by a typed model
(internal/artifacts): its top-level bullet list enumerates the target's artifacts.
Preserve the body structure (keep artifacts as top-level bullets) and edit in place
rather than restructuring, so the typed parse keeps working.`

const defaultFileContext = `This is an ordinary file in the beehive repository. Follow the conventions,
structure, and formatting already present in the file; make the smallest coherent
change that satisfies the request and do not reformat or restructure unrelated
content.`
