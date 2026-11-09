package agent

// RenderMarkdown snapshots the complete plan, including statuses and recorded
// evidence, for non-filesystem session backends.
func (p *PlanState) RenderMarkdown() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	goal, steps := p.snapshotLocked()
	return renderPlanMarkdownWithDoc(goal, p.doc, steps)
}
