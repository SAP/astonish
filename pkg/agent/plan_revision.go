package agent

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// Revision input is definition-only: callers cannot forge status or evidence,
// lifecycle, results, durable identity, or a recursively nested proposal.
type PlanStepDefinition struct {
	Name          string           `json:"name"`
	Description   string           `json:"description"`
	Details       string           `json:"details"`
	Summary       string           `json:"summary"`
	Files         []PlanFileChange `json:"files"`
	Outcome       string           `json:"outcome"`
	Verify        string           `json:"verify"`
	VerifyKind    string           `json:"verify_kind"`
	ParallelGroup string           `json:"parallel_group,omitempty"`
}

type PlanDocumentDefinition struct {
	Context      string `json:"context"`
	WhatNotToDo  string `json:"what_not_to_do"`
	Verification string `json:"verification"`
}

type PlanDefinition struct {
	Goal     string                 `json:"goal"`
	Document PlanDocumentDefinition `json:"document"`
	Steps    []PlanStepDefinition   `json:"steps"`
}

type PlanRevisionRequest struct {
	PlanID       string `json:"plan_id"`
	BaseRevision int64  `json:"base_revision"`
	Reason       string `json:"reason"`
	// Deprecated: decoded for compatibility, ignored. All valid revisions apply immediately.
	RequiresApproval bool                    `json:"requires_approval,omitempty"`
	Goal             *string                 `json:"goal,omitempty"`
	Document         *PlanDocumentDefinition `json:"document,omitempty"`
	Steps            []PlanStepDefinition    `json:"steps"`
}

type PlanRevisionDiff struct {
	GoalChanged      bool     `json:"goal_changed,omitempty"`
	DocumentChanged  bool     `json:"document_changed,omitempty"`
	StructureChanged bool     `json:"structure_changed,omitempty"`
	ChangedSteps     []string `json:"changed_steps,omitempty"`
	AddedSteps       []string `json:"added_steps,omitempty"`
	RemovedSteps     []string `json:"removed_steps,omitempty"`
}

type PlanRevisionProposal struct {
	ID           string           `json:"id"`
	BaseRevision int64            `json:"base_revision"`
	Reason       string           `json:"reason"`
	Candidate    PlanDefinition   `json:"candidate"`
	Diff         PlanRevisionDiff `json:"diff"`
}

type PlanRevisionDecision string

const (
	PlanRevisionApprove        PlanRevisionDecision = "approve"
	PlanRevisionReject         PlanRevisionDecision = "reject"
	PlanRevisionRequestChanges PlanRevisionDecision = "request_changes"
)

type PlanRevisionClassification string

const (
	PlanRevisionNoop             PlanRevisionClassification = "noop"
	PlanRevisionImmediate        PlanRevisionClassification = "immediate"
	PlanRevisionApprovalRequired PlanRevisionClassification = "approval_required"
)

type PlanRevisionResultKind string

const (
	PlanRevisionResultNoop             PlanRevisionResultKind = "noop"
	PlanRevisionResultApplied          PlanRevisionResultKind = "applied"
	PlanRevisionResultProposed         PlanRevisionResultKind = "proposed"
	PlanRevisionResultRejected         PlanRevisionResultKind = "rejected"
	PlanRevisionResultChangesRequested PlanRevisionResultKind = "changes_requested"
)

type PlanRevisionResult struct {
	Kind     PlanRevisionResultKind `json:"kind"`
	Snapshot PlanSnapshot           `json:"snapshot"`
}

type PlanRevisionCandidate struct {
	Classification PlanRevisionClassification `json:"classification"`
	Definition     PlanDefinition             `json:"definition"`
	Diff           PlanRevisionDiff           `json:"diff"`
}

func (s PlanStepDefinition) StepInfo() PlanStepInfo {
	return PlanStepInfo{Name: s.Name, Description: s.Description, Details: s.Details, Summary: s.Summary, Files: slices.Clone(s.Files), Outcome: s.Outcome, Verify: s.Verify, VerifyKind: s.VerifyKind, ParallelGroup: s.ParallelGroup}
}

func (s PlanStepInfo) Definition() PlanStepDefinition {
	return PlanStepDefinition{Name: s.Name, Description: s.Description, Details: s.Details, Summary: s.Summary, Files: slices.Clone(s.Files), Outcome: s.Outcome, Verify: s.Verify, VerifyKind: s.VerifyKind, ParallelGroup: s.ParallelGroup}
}

func (d PlanDocumentInfo) Definition() PlanDocumentDefinition {
	return PlanDocumentDefinition{Context: d.Context, WhatNotToDo: d.WhatNotToDo, Verification: d.Verification}
}

func (s PlanSnapshot) Definition() PlanDefinition {
	d := PlanDefinition{Goal: s.Goal, Document: s.Document.Definition(), Steps: make([]PlanStepDefinition, len(s.Steps))}
	for i := range s.Steps {
		d.Steps[i] = s.Steps[i].Definition()
	}
	return d
}

func (d PlanDefinition) Clone() PlanDefinition {
	d.Steps = slices.Clone(d.Steps)
	for i := range d.Steps {
		d.Steps[i].Files = slices.Clone(d.Steps[i].Files)
	}
	return d
}

func (p *PlanRevisionProposal) Clone() *PlanRevisionProposal {
	if p == nil {
		return nil
	}
	c := *p
	c.Candidate = p.Candidate.Clone()
	c.Diff.ChangedSteps = slices.Clone(p.Diff.ChangedSteps)
	c.Diff.AddedSteps = slices.Clone(p.Diff.AddedSteps)
	c.Diff.RemovedSteps = slices.Clone(p.Diff.RemovedSteps)
	return &c
}

// CanonicalPlanPath compares file declarations lexically; it never touches disk,
// resolves symlinks, or authorizes execution. In-workspace paths are relative;
// external paths stay absolute when an absolute workspace is available. Without
// one, retain each path's relativity rather than guessing containment from CWD.
func CanonicalPlanPath(workspace, path string) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsAny(path, "\x00\r\n") {
		return "", fmt.Errorf("invalid file path %q", path)
	}
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == ".." {
		return "", fmt.Errorf("file path names a root: %q", path)
	}
	if filepath.IsAbs(workspace) {
		workspace = filepath.Clean(workspace)
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		if path == workspace {
			return "", fmt.Errorf("file path names workspace root: %q", path)
		}
		if rel, err := filepath.Rel(workspace, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			path = rel
		}
	}
	if filepath.IsAbs(path) && filepath.Dir(path) == path {
		return "", fmt.Errorf("file path names filesystem root: %q", path)
	}
	return filepath.ToSlash(path), nil
}

func canonicalRevisionStep(s PlanStepDefinition, workspace string) (PlanStepDefinition, error) {
	s.Files = slices.Clone(s.Files)
	seen := map[string]bool{}
	for i, f := range s.Files {
		p, err := CanonicalPlanPath(workspace, f.Path)
		if err != nil {
			return s, err
		}
		kind := strings.ToLower(strings.TrimSpace(f.Kind))
		switch kind {
		case "", "modify":
			kind = "modify"
		case "new", "delete":
		default:
			return s, fmt.Errorf("invalid file kind %q", f.Kind)
		}
		if seen[p] {
			return s, fmt.Errorf("duplicate file path %q", p)
		}
		seen[p] = true
		s.Files[i] = PlanFileChange{Path: p, Kind: kind}
	}
	// File declarations are a set, unlike ordered phases.
	slices.SortFunc(s.Files, func(a, b PlanFileChange) int { return strings.Compare(a.Path, b.Path) })
	return s, nil
}

// BuildPlanRevisionCandidate validates and classifies without mutating current,
// assigning identities, executing verification, or accepting a proposal.
// Historical graph acceptance is enforced at initial announcement, not revisions.
func BuildPlanRevisionCandidate(current PlanSnapshot, req PlanRevisionRequest, workspace, _ string) (PlanRevisionCandidate, error) {
	fail := func(msg string) (PlanRevisionCandidate, error) {
		return PlanRevisionCandidate{}, fmt.Errorf("plan revision rejected: %s", msg)
	}
	if req.PlanID == "" || req.PlanID != current.PlanID {
		return fail("plan_id does not match active plan")
	}
	if req.BaseRevision != current.Revision {
		return fail("stale base_revision")
	}
	if strings.TrimSpace(req.Reason) == "" {
		return fail("reason is required")
	}
	if len(req.Steps) == 0 {
		return fail("full replacement steps are required")
	}
	base := current.Definition()
	next := PlanDefinition{Goal: base.Goal, Document: base.Document, Steps: slices.Clone(req.Steps)}
	if req.Goal != nil {
		next.Goal = *req.Goal
	}
	if req.Document != nil {
		next.Document = *req.Document
	}
	if strings.TrimSpace(next.Goal) == "" && next.Goal != base.Goal {
		return fail("goal is required")
	}
	diff := PlanRevisionDiff{GoalChanged: next.Goal != base.Goal, DocumentChanged: next.Document != base.Document}
	if diff.DocumentChanged && (strings.TrimSpace(next.Document.Context) == "" || strings.TrimSpace(next.Document.WhatNotToDo) == "" || strings.TrimSpace(next.Document.Verification) == "") {
		return fail("changed document requires context, what_not_to_do and verification")
	}
	old := map[string]PlanStepDefinition{}
	for _, s := range base.Steps {
		old[s.Name] = s
	}
	seen := map[string]bool{}
	changed := map[string]bool{}
	for i, s := range next.Steps {
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Name) != s.Name || strings.ContainsAny(s.Name, "\r\n") {
			return fail("invalid phase name")
		}
		if seen[s.Name] {
			return fail("duplicate phase name: " + s.Name)
		}
		seen[s.Name] = true
		prev, exists := old[s.Name]
		// Legacy phases pass unchanged, even if their old contract is incomplete.
		if exists && reflect.DeepEqual(s, prev) {
			next.Steps[i] = s
		} else {
			canonical, err := canonicalRevisionStep(s, workspace)
			if err != nil {
				return fail(s.Name + ": " + err.Error())
			}
			next.Steps[i] = canonical
			oldCanonical, oldErr := canonicalRevisionStep(prev, workspace)
			if !exists || oldErr != nil || !reflect.DeepEqual(canonical, oldCanonical) {
				changed[s.Name] = true
				if exists {
					diff.ChangedSteps = append(diff.ChangedSteps, s.Name)
				} else {
					diff.AddedSteps = append(diff.AddedSteps, s.Name)
				}
			}
		}
		if i >= len(base.Steps) || base.Steps[i].Name != s.Name || (exists && prev.ParallelGroup != s.ParallelGroup) {
			diff.StructureChanged = true
		}
	}
	for _, s := range base.Steps {
		if !seen[s.Name] {
			diff.RemovedSteps = append(diff.RemovedSteps, s.Name)
			diff.StructureChanged = true
		}
	}
	// Reuse announced-plan contract checks for changed phases only. Supply prior
	// behavior context for delete ordering; legacy phases are not revalidated.
	validationDoc := PlanDocumentInfo{Context: "revision", WhatNotToDo: "revision", Verification: "revision"}
	for i, s := range next.Steps {
		if !changed[s.Name] {
			continue
		}
		if strings.TrimSpace(s.Description) == "" {
			return fail(s.Name + ": description is required")
		}
		steps := []PlanStepInfo{s.StepInfo()}
		priorBehavior := false
		for _, prior := range next.Steps[:i] {
			if NormalizeVerifyKind(prior.VerifyKind) == VerifyKindBehavior {
				priorBehavior = true
				break
			}
		}
		if priorBehavior {
			steps = append([]PlanStepInfo{{Name: "prior-behavior", Description: "prior", Details: "prior", Summary: "prior", Outcome: "prior", Verify: "prior behavior context", VerifyKind: VerifyKindBehavior, Files: []PlanFileChange{{Path: "prior-context", Kind: "modify"}}}}, steps...)
		}
		if msg := ValidateAnnouncedPlan(validationDoc, steps); msg != "" {
			return fail(msg)
		}
	}
	// Validate newly introduced cross-phase conflicts using canonical paths.
	for i, a := range next.Steps {
		for j := 0; j < i; j++ {
			b := next.Steps[j]
			if !changed[a.Name] && !changed[b.Name] {
				continue
			}
			if a.ParallelGroup == "" && b.ParallelGroup == "" && strings.TrimSpace(a.Verify) != "" && strings.TrimSpace(a.Verify) == strings.TrimSpace(b.Verify) {
				return fail("serial phases share verify command")
			}
			if a.ParallelGroup == "" || a.ParallelGroup != b.ParallelGroup {
				continue
			}
			for _, af := range a.Files {
				for _, bf := range b.Files {
					ap, ae := CanonicalPlanPath(workspace, af.Path)
					bp, be := CanonicalPlanPath(workspace, bf.Path)
					if ae == nil && be == nil && ap == bp {
						return fail("parallel phases share file: " + ap)
					}
				}
			}
		}
	}
	classification := PlanRevisionImmediate
	if !diff.GoalChanged && !diff.DocumentChanged && !diff.StructureChanged && len(diff.ChangedSteps) == 0 && len(diff.AddedSteps) == 0 && len(diff.RemovedSteps) == 0 {
		classification = PlanRevisionNoop
	}
	return PlanRevisionCandidate{Classification: classification, Definition: next.Clone(), Diff: diff}, nil
}
