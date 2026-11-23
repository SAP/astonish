package tui

// Palettes adapt publicly documented upstream colors; see code-theme-palette-research.md.
// Component geometry remains exclusively in buildCodeTheme.
const (
	CodeThemeTokyoNight     CodeThemeID = "tokyo-night"
	CodeThemeMocha          CodeThemeID = "catppuccin-mocha"
	CodeThemeNord           CodeThemeID = "nord"
	CodeThemeDracula        CodeThemeID = "dracula"
	CodeThemeGruvbox        CodeThemeID = "gruvbox"
	CodeThemeRosePine       CodeThemeID = "rose-pine"
	CodeThemeSolarizedLight CodeThemeID = "solarized-light"
	CodeThemeGitHubLight    CodeThemeID = "github-light"
)

type codeThemeDefinition struct {
	option         CodeThemeOption
	palette        codePalette
	added, removed string
}

var codeThemeCatalog = []codeThemeDefinition{
	{CodeThemeOption{CodeThemeBlueGray, "Blue-gray", true}, codePalette{"#101922", "#18232e", "#344552", "#e2edf5", "#9cafbf", "#ff8700", "#38df92", "#e5b766", "#ff7f87"}, "#1a3320", "#3d1f1f"},
	{CodeThemeOption{CodeThemeDarkOrange, "Dark orange", false}, codePalette{"#111416", "#1a1e21", "#444444", "#d0d0d0", "#8a8a8a", "#ff8700", "#87d787", "#af8700", "#ff5f5f"}, "#1a3320", "#3d1f1f"},
	{CodeThemeOption{CodeThemeTokyoNight, "Tokyo Night", false}, codePalette{"#1a1b26", "#24283b", "#414868", "#c0caf5", "#9aa5ce", "#7aa2f7", "#9ece6a", "#e0af68", "#f7768e"}, "#25332b", "#3b2636"},
	{CodeThemeOption{CodeThemeMocha, "Catppuccin Mocha", false}, codePalette{"#1e1e2e", "#313244", "#585b70", "#cdd6f4", "#a6adc8", "#cba6f7", "#a6e3a1", "#f9e2af", "#f38ba8"}, "#293a32", "#422b3d"},
	{CodeThemeOption{CodeThemeNord, "Nord", false}, codePalette{"#2e3440", "#3b4252", "#4c566a", "#eceff4", "#d8dee9", "#88c0d0", "#a3be8c", "#ebcb8b", "#ef9ba4"}, "#35483c", "#503740"},
	{CodeThemeOption{CodeThemeDracula, "Dracula-inspired", false}, codePalette{"#282a36", "#343746", "#6272a4", "#f8f8f2", "#a0aacb", "#bd93f9", "#50fa7b", "#f1fa8c", "#ff8080"}, "#284333", "#4a2e3b"},
	{CodeThemeOption{CodeThemeGruvbox, "Gruvbox", false}, codePalette{"#282828", "#3c3836", "#665c54", "#ebdbb2", "#bdae93", "#fe8019", "#b8bb26", "#fabd2f", "#ff8070"}, "#343f2d", "#4b302b"},
	{CodeThemeOption{CodeThemeRosePine, "Rosé Pine", false}, codePalette{"#191724", "#1f1d2e", "#524f67", "#e0def4", "#908caa", "#ebbcba", "#9ccfd8", "#f6c177", "#eb6f92"}, "#25363b", "#3d2639"},
	{CodeThemeOption{CodeThemeSolarizedLight, "Solarized Light-inspired", false}, codePalette{"#fdf6e3", "#eee8d5", "#93a1a1", "#073642", "#50656c", "#006b9e", "#526600", "#805900", "#b52825"}, "#e1ebd4", "#f4ddd3"},
	{CodeThemeOption{CodeThemeGitHubLight, "GitHub Light-inspired", false}, codePalette{"#ffffff", "#f6f8fa", "#d1d9e0", "#1f2328", "#59636e", "#0969da", "#1a7f37", "#9a6700", "#d1242f"}, "#dafbe1", "#ffebe9"},
}
