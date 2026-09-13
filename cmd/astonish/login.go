package astonish

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/SAP/astonish/pkg/client"
	"github.com/charmbracelet/huh"
	"golang.org/x/term"
)

func handleLoginCommand(args []string) error {
	if len(args) == 0 {
		fmt.Println("Usage: astonish login <server-url> [flags]")
		fmt.Println("")
		fmt.Println("Flags:")
		fmt.Println("  --sso           Open a browser for OAuth login (SSO or Studio password)")
		fmt.Println("  --org <slug>    Select organization (skip prompt)")
		fmt.Println("  --team <slug>   Select team (skip prompt)")
		fmt.Println("")
		fmt.Println("Examples:")
		fmt.Println("  astonish login https://astonish.mycompany.com")
		fmt.Println("  astonish login https://astonish.mycompany.com --sso")
		fmt.Println("  astonish login https://astonish.mycompany.com --org my-org --team backend")
		return fmt.Errorf("server URL required")
	}

	serverURL := strings.TrimRight(args[0], "/")
	useSSO := false
	flagOrg := ""
	flagTeam := ""

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--sso":
			useSSO = true
		case "--org":
			if i+1 < len(args) {
				flagOrg = args[i+1]
				i++
			}
		case "--team":
			if i+1 < len(args) {
				flagTeam = args[i+1]
				i++
			}
		}
	}

	// Validate URL
	if !strings.HasPrefix(serverURL, "http://") && !strings.HasPrefix(serverURL, "https://") {
		serverURL = "https://" + serverURL
	}

	// Check if already logged in
	if client.IsRemoteMode() {
		cfg, _ := client.LoadRemoteConfig()
		if cfg != nil {
			fmt.Printf("Already connected to %s as %s\n", cfg.URL, cfg.UserEmail)
			fmt.Printf("Run 'astonish logout' first to disconnect.\n")
			return fmt.Errorf("already connected")
		}
	}

	if useSSO {
		return handleOAuthLogin(serverURL)
	}

	// Interactive email/password prompt
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Email: ")
	email, _ := reader.ReadString('\n')
	email = strings.TrimSpace(email)

	fmt.Print("Password: ")
	passwordBytes, passErr := term.ReadPassword(int(syscall.Stdin))
	fmt.Println() // newline after hidden input
	if passErr != nil {
		return fmt.Errorf("failed to read password: %w", passErr)
	}
	password := string(passwordBytes)

	if email == "" || password == "" {
		return fmt.Errorf("email and password are required")
	}

	// Phase 1: Initial login to authenticate and get available orgs/teams
	result, err := client.LoginWithPassword(serverURL, email, password, flagOrg, flagTeam)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	// Phase 2: Interactive org selection (if multiple orgs and no flag provided)
	if flagOrg == "" && len(result.AvailableOrgs) > 1 {
		selectedOrg, err := promptOrgSelection(result.AvailableOrgs, result.OrgSlug)
		if err != nil {
			return fmt.Errorf("org selection: %w", err)
		}

		if selectedOrg != result.OrgSlug {
			// Re-login with the selected org to get a properly scoped token
			result, err = client.LoginWithPassword(serverURL, email, password, selectedOrg, "")
			if err != nil {
				return fmt.Errorf("login failed (org switch): %w", err)
			}
			if err := exchangePasswordLoginForOAuth(serverURL, result); err != nil {
				return err
			}
		}
	}

	// Phase 3: Interactive team selection (if multiple teams and no flag provided)
	if flagTeam == "" && len(result.AvailableTeams) > 1 {
		selectedTeam, err := promptTeamSelection(result.AvailableTeams, result.TeamSlug)
		if err != nil {
			return fmt.Errorf("team selection: %w", err)
		}

		if selectedTeam != result.TeamSlug {
			// Re-login with the selected org+team to get a properly scoped token
			result, err = client.LoginWithPassword(serverURL, email, password, result.OrgSlug, selectedTeam)
			if err != nil {
				return fmt.Errorf("login failed (team switch): %w", err)
			}
		}
	}

	if err := exchangePasswordLoginForOAuth(serverURL, result); err != nil {
		return err
	}

	printLoginSuccess(result)
	return nil
}

// promptOrgSelection shows an interactive selector for organizations.
func promptOrgSelection(orgs []client.LoginOrgOption, currentSlug string) (string, error) {
	options := make([]huh.Option[string], 0, len(orgs))
	for _, org := range orgs {
		label := fmt.Sprintf("%s (%s)", org.Name, org.Slug)
		if org.Role != "" && org.Role != "member" {
			label += fmt.Sprintf(" [%s]", org.Role)
		}
		options = append(options, huh.NewOption(label, org.Slug))
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select organization").
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}
	return selected, nil
}

// promptTeamSelection shows an interactive selector for teams.
func promptTeamSelection(teams []client.LoginTeamOption, currentSlug string) (string, error) {
	options := make([]huh.Option[string], 0, len(teams))
	for _, team := range teams {
		label := fmt.Sprintf("%s (%s)", team.Name, team.Slug)
		options = append(options, huh.NewOption(label, team.Slug))
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Select team").
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		return "", err
	}
	return selected, nil
}

// printLoginSuccess displays the login success message.
func printLoginSuccess(result *client.LoginResult) {
	fmt.Printf("\nLogged in as %s", result.UserEmail)
	if result.DisplayName != "" {
		fmt.Printf(" (%s)", result.DisplayName)
	}
	fmt.Println()

	orgDisplay := result.OrgSlug
	if result.OrgName != "" {
		orgDisplay = result.OrgName
	}
	fmt.Printf("Organization: %s\n", orgDisplay)
	if result.TeamSlug != "" {
		fmt.Printf("Team: %s\n", result.TeamSlug)
	}
}

func handleOAuthLogin(serverURL string) error {
	fmt.Println("Initiating OAuth login...")
	result, err := client.LoginWithOAuth(serverURL, func(status string) {
		switch status {
		case "opening_browser":
			fmt.Println("Opening browser for authentication...")
			fmt.Println("If the browser doesn't open, use the URL printed above.")
		case "browser_failed":
			fmt.Println("Could not open browser automatically.")
			fmt.Println("Please open the URL printed above in your browser.")
		case "polling":
			fmt.Println("Waiting for authentication to complete...")
			fmt.Println("(Complete the login in your browser)")
		}
	})
	if err != nil {
		return fmt.Errorf("OAuth login failed: %w", err)
	}
	printLoginSuccess(result)
	return nil
}

func exchangePasswordLoginForOAuth(serverURL string, result *client.LoginResult) error {
	ts, err := client.NewTokenStore()
	if err != nil {
		return fmt.Errorf("init token store: %w", err)
	}
	tokens, err := ts.Load()
	if err != nil || tokens == nil || tokens.AccessToken == "" {
		return fmt.Errorf("password login did not store a session token")
	}
	exchanged, err := client.ExchangePlatformSessionForOAuth(serverURL, tokens.AccessToken)
	if err != nil {
		return fmt.Errorf("exchange session for OAuth token: %w", err)
	}
	converted, err := client.PersistOAuthLogin(serverURL, exchanged)
	if err != nil {
		return err
	}
	if converted.UserEmail != "" {
		result.UserEmail = converted.UserEmail
	}
	if converted.OrgSlug != "" {
		result.OrgSlug = converted.OrgSlug
	}
	if converted.TeamSlug != "" {
		result.TeamSlug = converted.TeamSlug
	}
	return nil
}
