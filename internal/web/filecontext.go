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
//
// The repo-ROOT instruction files (root-instruction-file-links) are the ONE
// path-qualified exception: AGENTS.md, HONEYBEE.md, BOOTSTRAP.md, and LOCALS.md at
// the repo root each carry their own purpose/ownership preamble, matched only when
// the target is at the root (no directory). This keeps the generic root AGENTS.md
// (the operating guide) from being conflated with a per-submodule
// submodules/<sm>/AGENTS.md rules overlay, which keeps its overlay rule in the
// basename table below.

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

// rootFileContexts maps each repo-ROOT instruction file (repo.RootInstructionFiles)
// to its purpose/ownership preamble. It is consulted ONLY for a target at the repo
// root (path.Dir == "/"), so the generic root AGENTS.md resolves here to the
// operating-guide rules while a per-submodule submodules/<sm>/AGENTS.md still
// resolves to the overlay rule in fileContextRules — the two are never conflated.
// AGENTS/HONEYBEE/BOOTSTRAP note they are beehive-managed (shipped + refreshed by
// `beehive instruction update`); LOCALS.md notes it is site-authored and never
// auto-generated. Keyed off the same constants every other reader uses.
var rootFileContexts = map[string]string{
	repo.AgentsFile:    rootAgentsFileContext,
	repo.HoneybeeFile:  honeybeeFileContext,
	repo.BootstrapFile: bootstrapFileContext,
	repo.LocalsFile:    localsFileContext,
}

// resolveFileContext returns the editing preamble for a repo-relative path. A
// repo-ROOT instruction file (no directory component) matches rootFileContexts
// first, so the generic root AGENTS.md is not conflated with a per-submodule
// overlay; otherwise the path is matched by basename against fileContextRules, or
// the generic default when no rule matches. It is total: every path resolves to
// some non-empty preamble.
func resolveFileContext(repoPath string) string {
	clean := path.Clean("/" + repoPath)
	base := path.Base(clean)
	if path.Dir(clean) == "/" {
		if p, ok := rootFileContexts[base]; ok {
			return p
		}
	}
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

// The four constants below seed the repo-ROOT instruction files
// (root-instruction-file-links). Each is distinct and carries the file's purpose
// plus its ownership: AGENTS/HONEYBEE/BOOTSTRAP are beehive-MANAGED (shipped and
// refreshed by `beehive instruction update`), LOCALS.md is site-authored and never
// auto-generated. rootAgentsFileContext is the GENERIC root operating guide and is
// deliberately not the per-submodule AGENTS.md overlay (agentsFileContext above).

const rootAgentsFileContext = `This file is the repo-ROOT AGENTS.md — the GENERIC operating guide for any agent
working a beehive repo. It is NOT a per-submodule submodules/<sm>/AGENTS.md rules
overlay: keep it generic (what a beehive repo is, how to edit files without racing
the swarm, what the deterministic runner owns, the skills index) and do NOT put
site-specific facts here (those live in LOCALS.md) or the honeybee runtime protocol
(that is HONEYBEE.md). It is a beehive-MANAGED default: the binary ships it and
` + "`beehive instruction update`" + ` refreshes it, so keep edits an operating guide,
not local configuration. Make the smallest coherent edit; agents read it verbatim.`

const honeybeeFileContext = `This file is HONEYBEE.md — the honeybee runtime protocol the deterministic runner
injects as each pass's system prompt. Keep it the authoritative per-kind role
contract (reconcile / work / review / arbitration), the claim model, the exhaustive
status transitions, and the absolute rules; it is protocol, not site facts (those
live in LOCALS.md). It is a beehive-MANAGED default refreshed by
` + "`beehive instruction update`" + `. Preserve its structure and the exact status
strings; make the smallest coherent edit — honeybees follow this verbatim.`

const bootstrapFileContext = `This file is BOOTSTRAP.md — the step-by-step walkthrough to stand up a new beehive
install (locals, infrastructure, submodules, scheduler). Keep it an ordered,
imperative setup guide. The site-specific values it tells the operator to record
belong in LOCALS.md, not inlined here. It is a beehive-MANAGED default refreshed by
` + "`beehive instruction update`" + `; edit prose in place and keep the steps in order.`

const localsFileContext = `This file is LOCALS.md — the SITE-SPECIFIC operational record for THIS beehive
install: machine paths, build/deploy commands, scheduler units, hostnames/ports,
and local safety rules. It is AUTHORED PER INSTALL and is NOT beehive-managed —
` + "`beehive instruction update`" + ` never touches it and it is NEVER auto-generated.
Fill it with this install's real facts in the operator's own words; do not restate
the generic guide (AGENTS.md) or the runtime protocol (HONEYBEE.md). If a value is
unknown, leave a clearly-marked TODO rather than inventing site facts.`

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
