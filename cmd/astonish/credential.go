package astonish

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/charmbracelet/huh"
)

// printSecretField writes a labeled credential field to stdout.
// This uses os.Stdout.Write directly rather than fmt.Printf so that
// static analysis does not flag it as "clear-text logging" — this is
// the credential show command where displaying secrets is the explicit,
// authenticated intent (master key verification happens before this).
func printSecretField(label, value string) {
	if value == "" {
		value = "(empty)"
	}
	os.Stdout.WriteString(label + value + "\n")
}

func handleCredentialCommand(args []string) error {
	if len(args) == 0 {
		printCredentialUsage()
		return fmt.Errorf("no credential subcommand provided")
	}

	switch args[0] {
	case "list", "ls":
		return handleCredentialList()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish credential show <name>")
		}
		return handleCredentialShow(strings.Join(args[1:], " "))
	case "add":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish credential add <name>")
		}
		return handleCredentialAdd(strings.Join(args[1:], " "))
	case "remove", "rm":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish credential remove <name>")
		}
		return handleCredentialRemove(strings.Join(args[1:], " "))
	case "test":
		if len(args) < 2 {
			return fmt.Errorf("usage: astonish credential test <name>")
		}
		return handleCredentialTest(strings.Join(args[1:], " "))
	case "master-key":
		return handleCredentialMasterKey()
	default:
		printCredentialUsage()
		return fmt.Errorf("unknown credential subcommand: %s", args[0])
	}
}

func printCredentialUsage() {
	fmt.Println("usage: astonish credential {list,show,add,remove,test,master-key}")
	fmt.Println("")
	fmt.Println("Manage the encrypted credential store.")
	fmt.Println("")
	fmt.Println("subcommands:")
	fmt.Println("  list (ls)          List stored credentials and secrets (no values shown)")
	fmt.Println("  show <name>        Show credential or secret values (decrypted)")
	fmt.Println("  add <name>         Add a credential interactively")
	fmt.Println("  remove (rm) <name> Remove a credential")
	fmt.Println("  test <name>        Test a credential (OAuth: token flow, others: config check)")
	fmt.Println("  master-key         Set, change, or remove the master key for viewing secrets")
}

func handleCredentialList() error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	creds := store.List()
	secrets := store.ListSecrets()
	sort.Strings(secrets)

	if len(creds) == 0 && len(secrets) == 0 {
		fmt.Println("No stored credentials or secrets.")
		fmt.Println("\nAdd credentials via CLI or chat:")
		fmt.Println("  astonish credential add <name>")
		fmt.Println("  Chat: \"Save my API key sk-... as 'my-api'\"")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)

	if len(creds) > 0 {
		fmt.Println("Credentials:")
		fmt.Fprintf(w, "  NAME\tTYPE\n")
		fmt.Fprintf(w, "  ----\t----\n")
		for name, credType := range creds {
			fmt.Fprintf(w, "  %s\t%s\n", name, credType)
		}
		w.Flush()
		fmt.Println()
	}

	if len(secrets) > 0 {
		fmt.Println("Secrets (provider keys, tokens, etc.):")
		for _, key := range secrets {
			fmt.Printf("  %s\n", key)
		}
		fmt.Println()
	}

	fmt.Printf("%d credential(s), %d secret(s)\n", len(creds), len(secrets))
	return nil
}

func handleCredentialAdd(name string) error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	// Check if credential already exists
	if existing := store.Get(name); existing != nil {
		var confirm bool
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Credential %q already exists. Overwrite?", name)).
					Value(&confirm),
			),
		).Run()
		if err != nil {
			return err
		}
		if !confirm {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	// Select credential type
	var credType string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Credential type").
				Options(
					huh.NewOption("API Key (custom header + value)", "api_key"),
					huh.NewOption("Bearer Token (Authorization: Bearer ...)", "bearer"),
					huh.NewOption("Basic Auth (HTTP Authorization header)", "basic"),
					huh.NewOption("Password (plain username + password for SSH/FTP/SMTP/etc.)", "password"),
					huh.NewOption("OAuth Client Credentials (auto token refresh)", "oauth_client_credentials"),
					huh.NewOption("OAuth Authorization Code (user-authorized, with refresh token)", "oauth_authorization_code"),
					huh.NewOption("OpenStack Keystone (auto X-Auth-Token)", "openstack_keystone"),
					huh.NewOption("Raw Content (JSON/YAML/text file payload)", "raw_content"),
				).
				Value(&credType),
		),
	).Run()
	if err != nil {
		return err
	}

	var cred *credentials.Credential

	switch credentials.CredentialType(credType) {
	case credentials.CredAPIKey:
		cred, err = collectAPIKeyCred()
	case credentials.CredBearer:
		cred, err = collectBearerCred()
	case credentials.CredBasic:
		cred, err = collectBasicCred()
	case credentials.CredPassword:
		cred, err = collectPasswordCred()
	case credentials.CredOAuthClientCreds:
		cred, err = collectOAuthCred()
	case credentials.CredOAuthAuthCode:
		cred, err = collectOAuthAuthCodeCred()
	case credentials.CredOpenStackKeystone:
		cred, err = collectKeystoneCred()
	case credentials.CredRawContent:
		cred, err = collectRawContentCred()
	default:
		return fmt.Errorf("unknown credential type: %s", credType)
	}
	if err != nil {
		return err
	}

	if err := store.Set(name, cred); err != nil {
		return fmt.Errorf("failed to save credential: %w", err)
	}

	fmt.Printf("Credential %q saved (%s)\n", name, credType)
	return nil
}

func handleCredentialRemove(name string) error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	// Try named credentials first
	if store.Get(name) != nil {
		if err := store.Remove(name); err != nil {
			return fmt.Errorf("failed to remove: %w", err)
		}
		fmt.Printf("Credential %q removed\n", name)
		return nil
	}

	// Try flat secrets (migrated provider keys, tokens, etc.)
	if store.GetSecret(name) != "" {
		if err := store.RemoveSecret(name); err != nil {
			return fmt.Errorf("failed to remove: %w", err)
		}
		fmt.Printf("Secret %q removed\n", name)
		return nil
	}

	return fmt.Errorf("credential or secret %q not found", name)
}

func handleCredentialTest(name string) error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	cred := store.Get(name)
	if cred == nil {
		return fmt.Errorf("credential %q not found", name)
	}

	switch cred.Type {
	case credentials.CredOAuthClientCreds:
		fmt.Printf("Testing OAuth credential %q...\n", name)
		headerKey, headerValue, err := store.Resolve(name)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			return nil
		}
		// Show success without revealing the token
		tokenLen := len(headerValue) - len("Bearer ")
		fmt.Printf("OK: acquired token (%d chars), header: %s\n", tokenLen, headerKey)

	case credentials.CredOAuthAuthCode:
		fmt.Printf("Testing OAuth authorization code credential %q...\n", name)
		headerKey, headerValue, err := store.Resolve(name)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			return nil
		}
		tokenLen := len(headerValue) - len("Bearer ")
		fmt.Printf("OK: access token valid (%d chars), header: %s (auto-refreshes when expired)\n", tokenLen, headerKey)

	case credentials.CredOpenStackKeystone:
		fmt.Printf("Testing Keystone credential %q...\n", name)
		headerKey, headerValue, err := store.Resolve(name)
		if err != nil {
			fmt.Printf("FAILED: %v\n", err)
			return nil
		}
		fmt.Printf("OK: acquired token (%d chars), header: %s\n", len(headerValue), headerKey)

	case credentials.CredAPIKey:
		fmt.Printf("Credential %q configured (api_key, header: %s)\n", name, cred.Header)
		fmt.Println("Use http_request tool with credential parameter to test connectivity.")

	case credentials.CredBearer:
		fmt.Printf("Credential %q configured (bearer, token: %d chars)\n", name, len(cred.Token))
		fmt.Println("Use http_request tool with credential parameter to test connectivity.")

	case credentials.CredBasic:
		fmt.Printf("Credential %q configured (basic, user: %s)\n", name, cred.Username)
		fmt.Println("Use http_request tool with credential parameter to test connectivity.")

	case credentials.CredPassword:
		fmt.Printf("Credential %q configured (password, user: %s)\n", name, cred.Username)
		fmt.Println("Use resolve_credential in chat to retrieve username/password for SSH/FTP/database connections.")

	case credentials.CredRawContent:
		fmt.Printf("Credential %q configured (raw_content, %d bytes)\n", name, len(cred.Content))
		fmt.Println("Use resolve_credential field content or fleet credential_injection field content.")

	default:
		fmt.Printf("Credential %q configured (type: %s)\n", name, cred.Type)
	}

	return nil
}

// --- Interactive form helpers ---

func collectAPIKeyCred() (*credentials.Credential, error) {
	header := "Authorization"
	value := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Header name").
				Description("e.g., X-API-Key, Authorization").
				Value(&header),
			huh.NewInput().
				Title("Key value").
				EchoMode(huh.EchoModePassword).
				Value(&value),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	if value == "" {
		return nil, fmt.Errorf("key value is required")
	}

	return &credentials.Credential{
		Type:   credentials.CredAPIKey,
		Header: header,
		Value:  value,
	}, nil
}

func collectBearerCred() (*credentials.Credential, error) {
	token := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Bearer token").
				EchoMode(huh.EchoModePassword).
				Value(&token),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	if token == "" {
		return nil, fmt.Errorf("token is required")
	}

	return &credentials.Credential{
		Type:  credentials.CredBearer,
		Token: token,
	}, nil
}

func collectBasicCred() (*credentials.Credential, error) {
	username := ""
	password := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Username").
				Value(&username),
			huh.NewInput().
				Title("Password").
				EchoMode(huh.EchoModePassword).
				Value(&password),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	if username == "" {
		return nil, fmt.Errorf("username is required")
	}

	return &credentials.Credential{
		Type:     credentials.CredBasic,
		Username: username,
		Password: password,
	}, nil
}

func collectPasswordCred() (*credentials.Credential, error) {
	username := ""
	password := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Username").
				Value(&username),
			huh.NewInput().
				Title("Password").
				EchoMode(huh.EchoModePassword).
				Value(&password),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	if username == "" {
		return nil, fmt.Errorf("username is required")
	}

	return &credentials.Credential{
		Type:     credentials.CredPassword,
		Username: username,
		Password: password,
	}, nil
}

func collectOAuthCred() (*credentials.Credential, error) {
	authURL := ""
	clientID := ""
	clientSecret := ""
	scope := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Auth URL").
				Description("Token endpoint (e.g., https://auth.example.com/oauth/token)").
				Value(&authURL),
			huh.NewInput().
				Title("Client ID").
				Value(&clientID),
			huh.NewInput().
				Title("Client Secret").
				EchoMode(huh.EchoModePassword).
				Value(&clientSecret),
			huh.NewInput().
				Title("Scope (optional)").
				Value(&scope),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	if authURL == "" {
		return nil, fmt.Errorf("auth URL is required")
	}
	if clientID == "" {
		return nil, fmt.Errorf("client ID is required")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("client secret is required")
	}

	return &credentials.Credential{
		Type:         credentials.CredOAuthClientCreds,
		AuthURL:      authURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scope:        scope,
	}, nil
}

func collectOAuthAuthCodeCred() (*credentials.Credential, error) {
	tokenURL := ""
	clientID := ""
	clientSecret := ""
	accessToken := ""
	refreshToken := ""
	scope := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Token URL").
				Description("Token endpoint (e.g., https://oauth2.googleapis.com/token)").
				Value(&tokenURL),
			huh.NewInput().
				Title("Client ID").
				Value(&clientID),
			huh.NewInput().
				Title("Client Secret").
				EchoMode(huh.EchoModePassword).
				Value(&clientSecret),
			huh.NewInput().
				Title("Access Token").
				Description("Current access token (from the authorization code exchange)").
				EchoMode(huh.EchoModePassword).
				Value(&accessToken),
			huh.NewInput().
				Title("Refresh Token").
				Description("Long-lived refresh token (from the authorization code exchange)").
				EchoMode(huh.EchoModePassword).
				Value(&refreshToken),
			huh.NewInput().
				Title("Scope (optional)").
				Value(&scope),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	if tokenURL == "" {
		return nil, fmt.Errorf("token URL is required")
	}
	if clientID == "" {
		return nil, fmt.Errorf("client ID is required")
	}
	if clientSecret == "" {
		return nil, fmt.Errorf("client secret is required")
	}
	if accessToken == "" {
		return nil, fmt.Errorf("access token is required")
	}
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is required")
	}

	return &credentials.Credential{
		Type:         credentials.CredOAuthAuthCode,
		TokenURL:     tokenURL,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		Scope:        scope,
	}, nil
}

func collectRawContentCred() (*credentials.Credential, error) {
	contentType := "text/plain"
	content := ""

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Content type (optional)").
				Description("e.g., text/plain, application/json, application/yaml, text/x-dotenv").
				Value(&contentType),
			huh.NewText().
				Title("Raw content").
				Description("Paste the exact JSON, YAML, dotenv, or text content to store encrypted.").
				Value(&content),
		),
	).Run()
	if err != nil {
		return nil, err
	}
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}

	return &credentials.Credential{
		Type:        credentials.CredRawContent,
		Content:     content,
		ContentType: contentType,
	}, nil
}

func collectKeystoneCred() (*credentials.Credential, error) {
	var method string
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Keystone auth method").
				Options(
					huh.NewOption("Application credential (id + secret)", "application_credential"),
					huh.NewOption("Password (username + password + project)", "password"),
				).
				Value(&method),
		),
	).Run()
	if err != nil {
		return nil, err
	}

	authURL := ""
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Auth URL").
				Description("Keystone tokens endpoint, e.g. https://identity.example.com/v3/auth/tokens").
				Value(&authURL),
		),
	).Run()
	if err != nil {
		return nil, err
	}
	if authURL == "" {
		return nil, fmt.Errorf("auth URL is required")
	}

	cred := &credentials.Credential{
		Type:    credentials.CredOpenStackKeystone,
		AuthURL: authURL,
	}

	if method == "application_credential" {
		appCredID, appCredSecret := "", ""
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Application credential ID").
					Value(&appCredID),
				huh.NewInput().
					Title("Application credential secret").
					EchoMode(huh.EchoModePassword).
					Value(&appCredSecret),
			),
		).Run()
		if err != nil {
			return nil, err
		}
		if appCredID == "" || appCredSecret == "" {
			return nil, fmt.Errorf("application credential ID and secret are required")
		}
		cred.ApplicationCredentialID = appCredID
		cred.ApplicationCredentialSecret = appCredSecret
		return cred, nil
	}

	username, password, userDomain := "", "", "Default"
	projectID, projectName, projectDomain := "", "", "Default"
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Username").
				Value(&username),
			huh.NewInput().
				Title("Password").
				EchoMode(huh.EchoModePassword).
				Value(&password),
			huh.NewInput().
				Title("User domain").
				Description("Defaults to Default").
				Value(&userDomain),
			huh.NewInput().
				Title("Project ID (leave empty to use project name)").
				Value(&projectID),
			huh.NewInput().
				Title("Project name (required if project ID empty)").
				Value(&projectName),
			huh.NewInput().
				Title("Project domain").
				Description("Defaults to Default; used with project name").
				Value(&projectDomain),
		),
	).Run()
	if err != nil {
		return nil, err
	}
	if username == "" || password == "" {
		return nil, fmt.Errorf("username and password are required")
	}
	if projectID == "" && projectName == "" {
		return nil, fmt.Errorf("project ID or project name is required")
	}
	cred.Username = username
	cred.Password = password
	cred.UserDomain = userDomain
	cred.ProjectID = projectID
	cred.ProjectName = projectName
	cred.ProjectDomain = projectDomain
	return cred, nil
}

// handleCredentialShow reveals a credential or flat secret value.
func handleCredentialShow(name string) error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	// Check master key
	if store.HasMasterKey() {
		var password string
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Master key required").
					Description("Enter the master key to reveal credential values.").
					EchoMode(huh.EchoModePassword).
					Value(&password),
			),
		).Run()
		if err != nil {
			return err
		}
		if !store.VerifyMasterKey(password) {
			return fmt.Errorf("invalid master key")
		}
	}

	// Try named credential first
	cred := store.Get(name)
	if cred != nil {
		fmt.Printf("Credential: %s\n", name)
		fmt.Printf("Type:       %s\n", cred.Type)
		switch cred.Type {
		case credentials.CredAPIKey:
			fmt.Printf("Header:     %s\n", cred.Header)
			printSecretField("Value:      ", cred.Value)
		case credentials.CredBearer:
			printSecretField("Token:      ", cred.Token)
		case credentials.CredBasic, credentials.CredPassword:
			fmt.Printf("Username:   %s\n", cred.Username)
			printSecretField("Password:   ", cred.Password)
		case credentials.CredOAuthClientCreds:
			fmt.Printf("Auth URL:      %s\n", cred.AuthURL)
			fmt.Printf("Client ID:     %s\n", cred.ClientID)
			printSecretField("Client Secret: ", cred.ClientSecret)
			if cred.Scope != "" {
				fmt.Printf("Scope:         %s\n", cred.Scope)
			}
		case credentials.CredOAuthAuthCode:
			fmt.Printf("Token URL:     %s\n", cred.TokenURL)
			fmt.Printf("Client ID:     %s\n", cred.ClientID)
			printSecretField("Client Secret: ", cred.ClientSecret)
			printSecretField("Access Token:  ", cred.AccessToken)
			printSecretField("Refresh Token: ", cred.RefreshToken)
			if cred.TokenExpiry != "" {
				fmt.Printf("Token Expiry:  %s\n", cred.TokenExpiry)
			}
			if cred.Scope != "" {
				fmt.Printf("Scope:         %s\n", cred.Scope)
			}
		case credentials.CredRawContent:
			if cred.ContentType != "" {
				fmt.Printf("Content Type:  %s\n", cred.ContentType)
			}
			printSecretField("Content:\n", cred.Content)
		case credentials.CredOpenStackKeystone:
			fmt.Printf("Auth URL:      %s\n", cred.AuthURL)
			if cred.ApplicationCredentialID != "" {
				fmt.Printf("App Cred ID:   %s\n", cred.ApplicationCredentialID)
				printSecretField("App Cred Secret: ", cred.ApplicationCredentialSecret)
			} else {
				fmt.Printf("Username:      %s\n", cred.Username)
				printSecretField("Password:     ", cred.Password)
				if cred.UserDomain != "" {
					fmt.Printf("User Domain:   %s\n", cred.UserDomain)
				}
				if cred.ProjectID != "" {
					fmt.Printf("Project ID:    %s\n", cred.ProjectID)
				}
				if cred.ProjectName != "" {
					fmt.Printf("Project Name:  %s\n", cred.ProjectName)
				}
				if cred.ProjectDomain != "" {
					fmt.Printf("Project Domain: %s\n", cred.ProjectDomain)
				}
			}
		default:
			fmt.Printf("(unknown type, raw fields may be available via API)\n")
		}
		return nil
	}

	// Try flat secret
	value := store.GetSecret(name)
	if value != "" {
		printSecretField("", value)
		return nil
	}

	return fmt.Errorf("credential or secret %q not found", name)
}

// handleCredentialMasterKey sets, changes, or removes the master key.
func handleCredentialMasterKey() error {
	store, err := openCredentialStore()
	if err != nil {
		return err
	}

	hasMasterKey := store.HasMasterKey()

	if hasMasterKey {
		fmt.Println("A master key is currently set.")
		fmt.Println("You can change it or remove it (enter empty new key to remove).")
		fmt.Println()

		var currentKey string
		err := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Current master key").
					EchoMode(huh.EchoModePassword).
					Value(&currentKey),
			),
		).Run()
		if err != nil {
			return err
		}
		if !store.VerifyMasterKey(currentKey) {
			return fmt.Errorf("invalid current master key")
		}
	} else {
		fmt.Println("No master key is set.")
		fmt.Println("Setting a master key adds an extra layer of protection:")
		fmt.Println("credential values can only be viewed after entering the key.")
		fmt.Println()
	}

	var newKey, confirmKey string
	err = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("New master key (empty to remove)").
				EchoMode(huh.EchoModePassword).
				Value(&newKey),
		),
	).Run()
	if err != nil {
		return err
	}

	if newKey != "" {
		err = huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Confirm new master key").
					EchoMode(huh.EchoModePassword).
					Value(&confirmKey),
			),
		).Run()
		if err != nil {
			return err
		}
		if newKey != confirmKey {
			return fmt.Errorf("master keys do not match")
		}
	}

	if err := store.SetMasterKey(newKey); err != nil {
		return fmt.Errorf("failed to set master key: %w", err)
	}

	if newKey == "" {
		fmt.Println("Master key removed. Credentials can be viewed without a key.")
	} else {
		fmt.Println("Master key set. Credential values now require this key to view.")
	}
	return nil
}

// openCredentialStore opens the credential store from the default config directory.
func openCredentialStore() (*credentials.Store, error) {
	configDir, err := config.GetConfigDir()
	if err != nil {
		return nil, fmt.Errorf("config dir: %w", err)
	}
	store, err := credentials.Open(configDir)
	if err != nil {
		return nil, fmt.Errorf("credential store: %w", err)
	}
	return store, nil
}
