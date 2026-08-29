package themes

// DefaultStyleRules returns the standardized best-practice rules that supplement
// any template-specific analysis. These are always included in the style guide
// markdown regardless of what the template analysis found.
func DefaultStyleRules() string {
	return `## Universal Slide Authoring Rules

### Content Density
- Maximum 6 bullet points per slide (Presentation Zen 6×6 rule)
- Maximum 6 words per bullet point
- 6×6 is a bullet cap, not permission to leave the rest of the canvas blank
- One key idea per slide, fully developed: a takeaway headline plus 2–4 content blocks
- Empty canvas is a defect — prefer a layout type that fills the page over a title on white
- If bullets exceed 6×6, split across slides; if a slide is sparse, pick a denser layout type instead of adding empty dividers

### Typography Hierarchy
- Title/headline: largest size, boldest weight — states the TAKEAWAY, not a topic label
- Subheading: 60-70% of title size — provides context or qualifier
- Body text: comfortable reading size (never below 24px on 1920×1080)
- Caption/label: smallest, often uppercase with letter-spacing for visual distinction
- Never use more than 3 type sizes on a single slide
- Title text should be a complete sentence takeaway ("Revenue grew 23% YoY"), not a topic label ("Revenue")

### Color Discipline
- Accent color: maximum ONE highlight per slide for emphasis
- Do not use accent color for body text — reserve for metrics, callouts, or key phrases
- Charts/data: use the template's accent palette; never introduce random colors
- Backgrounds: stay consistent with the template's surface color across all slides
- Use muted/subtle colors for supporting elements (dividers, card backgrounds)

### Spacing & Alignment
- All text left-aligned unless the template explicitly uses center-align (e.g., cover titles)
- Consistent left margin across all slides (creates visual rhythm when presenting)
- Related items share an edge and cluster; unrelated items have clear separation
- Vertical spacing between sections should be at least 2× the spacing within sections
- Maintain consistent padding inside cards and content boxes

### Charts & Data Visualization
- Strip all chartjunk: no 3-D effects, no gradient fills, no decorative grid lines
- Data-ink ratio > 0.7 (every visual element should carry information)
- Label data directly on the chart, not in a separate legend when possible
- Use the template's accent colors for chart elements — never random or default colors
- Prefer horizontal bar charts for comparisons (easier to read labels)
- Round numbers for display (show $1.2M not $1,203,847)

### Content Writing Style
- Slide titles are takeaway sentences, not topic labels
- Numbers speak louder than adjectives ("grew 47%" not "grew significantly")
- Comparisons need context (vs. what? vs. when? vs. whom?)
- Every metric needs a unit, timeframe, and comparison baseline
- Attribution and sources in small caption text at slide bottom

### What NOT to Generate (Universal Avoid List)
- Drop shadows on any element
- 3-D chart effects, bevels, or embossing
- Decorative clip-art, stock icons, or placeholder images
- Gradient fills on text
- Double-Y axis charts (unless mathematically required and explicitly requested)
- "Thank you" slides without a clear CTA or next step
- Topic-label titles ("Q3 Results") — always write takeaway sentences ("Q3 missed plan by 4pts")
- Walls of text or long paragraphs — use structured elements (bullets, tables, metrics)
- More than 2 font families on a single slide
- Inconsistent margins between slides (all slides should share the same content start position)
- Orphaned single words on a line (rewrite to avoid)
- All-caps body text (reserve uppercase for short labels/eyebrows only)
`
}
