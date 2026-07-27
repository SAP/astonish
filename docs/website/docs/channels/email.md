# Email

The Email channel allows users to interact with Astonish agents by sending and receiving standard email. It supports both traditional IMAP/SMTP and Microsoft 365 via the Graph API.

## Setup

### Option A: IMAP/SMTP

#### 1. Configure via CLI

Run the interactive setup wizard:

```bash
astonish channels setup email
```

The wizard collects IMAP/SMTP details, tests the IMAP connection, and stores credentials securely in the encrypted credential store.

Alternatively, configure manually in your config file:

```yaml
channels:
  email:
    enabled: true
    provider: "imap"
    imap_server: "imap.example.com:993"
    smtp_server: "smtp.example.com:587"
    address: "bot@example.com"
    username: "bot@example.com"
    poll_interval: 30           # Seconds between inbox checks
    allow_from:
      - "user@company.com"
      - "*@company.com"         # Wildcard: all users at domain
```

Passwords are stored in the encrypted credential store, not in the config file.

#### 2. Start the Daemon

```bash
astonish daemon start
```

The daemon polls the IMAP inbox at the configured interval and processes new messages.

### Option B: Microsoft 365 (Graph API)

Use this option for Microsoft 365 mailboxes. It uses the Microsoft Graph API with delegated OAuth2 permissions — no IMAP/SMTP configuration required.

#### 1. Register an App in Microsoft Entra ID

1. Go to [Azure Portal > App Registrations](https://portal.azure.com/#view/Microsoft_AAD_RegisteredApps/ApplicationsListBlade) and select **New registration**
2. Name it (e.g., "Astonish Email") and set the supported account type to **Single tenant**
3. No redirect URI is needed — click **Register**
4. Under **API permissions**, add Microsoft Graph **Delegated** permissions:
   - `Mail.ReadWrite` — Read and write mail
   - `Mail.Send` — Send mail
   - `offline_access` — Maintain access (refresh tokens)
   - `User.Read` — Sign in and read user profile (used for connection testing)
5. Click **Grant admin consent** for your organization
6. Under **Authentication**, enable **Allow public client flows** (set to **Yes**)

This allows the Device Code flow without a client secret. If you prefer to keep the app as a confidential client, create a client secret under **Certificates & secrets** instead.

#### 2. Obtain a Refresh Token

Run the following script to authenticate as your service account and obtain a refresh token. You need `curl` and `jq` installed.

```bash
#!/bin/bash

# ==========================================
# Configuration for Astonish Service Account
# ==========================================
TENANT_ID="your_tenant_id_here"
CLIENT_ID="your_client_id_here"

# Leave this BLANK ("") if you enabled "Allow public client flows" in Entra ID.
# Fill this in with your secret value if you are keeping it as a Confidential Client.
CLIENT_SECRET=""

# Scopes for full Mail functionality and token refreshing
SCOPES="offline_access https://graph.microsoft.com/Mail.Send https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/User.Read"

# ==========================================
# 1. Request the Device Code
# ==========================================
echo "Requesting device code from Microsoft..."
DEVICE_CODE_RESPONSE=$(curl -s -X POST "https://login.microsoftonline.com/$TENANT_ID/oauth2/v2.0/devicecode" \
  -d "client_id=$CLIENT_ID" \
  -d "scope=$SCOPES")

USER_CODE=$(echo "$DEVICE_CODE_RESPONSE" | jq -r .user_code)
DEVICE_CODE=$(echo "$DEVICE_CODE_RESPONSE" | jq -r .device_code)
MESSAGE=$(echo "$DEVICE_CODE_RESPONSE" | jq -r .message)
INTERVAL=$(echo "$DEVICE_CODE_RESPONSE" | jq -r .interval)

if [ "$USER_CODE" == "null" ] || [ -z "$USER_CODE" ]; then
    echo "Failed to get device code. Check your Client ID and Tenant ID."
    echo "$DEVICE_CODE_RESPONSE"
    exit 1
fi

echo ""
echo "=================================================="
echo "ACTION REQUIRED"
echo "$MESSAGE"
echo "=================================================="
echo ""

# ==========================================
# 2. Build the Token Request
# ==========================================
TOKEN_PARAMS=(
  "-d" "grant_type=urn:ietf:params:oauth:grant-type:device_code"
  "-d" "client_id=$CLIENT_ID"
  "-d" "device_code=$DEVICE_CODE"
)

if [ -n "$CLIENT_SECRET" ]; then
  TOKEN_PARAMS+=("-d" "client_secret=$CLIENT_SECRET")
fi

# ==========================================
# 3. Poll the Token Endpoint
# ==========================================
echo "Polling for token (waiting for you to log in and approve in the browser)..."

while true; do
    TOKEN_RESPONSE=$(curl -s -X POST "https://login.microsoftonline.com/$TENANT_ID/oauth2/v2.0/token" \
      "${TOKEN_PARAMS[@]}")

    ERROR=$(echo "$TOKEN_RESPONSE" | jq -r .error)

    if [ "$ERROR" == "authorization_pending" ]; then
        sleep "$INTERVAL"

    elif [ "$ERROR" == "null" ] || [ -z "$ERROR" ]; then
        REFRESH_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r .refresh_token)
        ACCESS_TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r .access_token)

        echo ""
        echo "Authentication Successful!"
        echo ""
        echo "REFRESH TOKEN (save this in Astonish):"
        echo "$REFRESH_TOKEN"
        echo ""
        echo "ACCESS TOKEN (for testing, expires in ~1 hour):"
        echo "$ACCESS_TOKEN"
        break

    elif [ "$ERROR" == "expired_token" ]; then
        echo ""
        echo "Device code expired. Run the script again."
        break

    else
        echo ""
        echo "Authorization failed: $ERROR"
        echo "$TOKEN_RESPONSE" | jq .
        break
    fi
done
```

Sign in as the **service account** (the mailbox Astonish will use) when prompted. After approval, the script prints the refresh token.

#### 3. Configure in Studio

1. Open **Studio > Settings > Channels > Email**
2. Select **Microsoft 365 (Graph API)** from the Provider dropdown
3. Fill in the connection details:
   - **Email Address** — The service account's mailbox (e.g., `bot@yourorg.onmicrosoft.com`)
4. Fill in the secrets:
   - **Tenant ID** — Your Azure AD tenant ID (from the app registration overview)
   - **Client ID** — The Application (client) ID from your app registration
   - **Client Secret** — Leave empty if using a public client app
   - **Refresh Token** — The token obtained from the script above
5. Click **Test Connection** to verify — this exchanges the refresh token and calls the Graph API
6. Click **Save** and toggle the channel to **Enabled**

Astonish automatically refreshes the access token and persists rotated refresh tokens. No manual token rotation is needed.

#### 4. Start the Daemon

```bash
astonish daemon start
```

The daemon polls the Graph API inbox at the configured interval.

## Configuration Options

| Option | Description | Provider | Default |
|--------|-------------|----------|---------|
| `provider` | Email provider type (`imap` or `msgraph`) | All | `"imap"` |
| `address` | Bot's email address | All | Required |
| `poll_interval` | Seconds between inbox checks | All | `30` |
| `allow_from` | Allowed sender addresses (supports `*` wildcards) | All | `[]` |
| `folder` | Folder to monitor | All | `"INBOX"` |
| `mark_read` | Mark processed messages as read | All | `true` |
| `max_body_chars` | Maximum email body length to process | All | `50000` |
| `imap_server` | IMAP server (host:port) | IMAP | Required |
| `smtp_server` | SMTP server (host:port) | IMAP | Required |
| `username` | IMAP/SMTP username | IMAP | Required |

Microsoft Graph secrets (Tenant ID, Client ID, Client Secret, Refresh Token) are stored as platform secrets and managed through the Studio UI.

## Email Processing Pipeline

1. **Poll** — Check for unread messages (IMAP inbox or Graph API)
2. **Filter** — Verify sender against allowlist
3. **Parse** — Extract text content (plain text preferred, HTML stripped as fallback)
4. **Route** — Determine org/team context from plus-addressing or user identity
5. **Execute** — Send content to agent engine
6. **Reply** — Format agent response and send as a reply (SMTP or Graph API)

Email threads (based on `In-Reply-To`/`References` headers) maintain the same agent session.

In PostgreSQL platform deployments, a verified email channel identity is resolved to the linked Astonish user. Agent tools invoked from that email can read that user's personal credentials first, then fall back to the routed team's credentials. This matches Studio chat behavior, so credentials saved privately in **Settings → Credentials** are available when the same user interacts by email. Email-triggered sandbox sessions also inherit the platform/org/team network allow rules and OpenShell gateway pre-seed configuration for the routed context.

## Plus-Addressing Routing (PostgreSQL)

In PostgreSQL deployments, the email channel supports plus-addressing to route messages to specific organizations:

```
bot+acme-corp@example.com          → Routes to org "acme-corp"
bot+acme-corp+backend@example.com  → Routes to org "acme-corp", team "backend"
bot@example.com                    → Routes to sender's default org
```

This allows users to control routing per-message without any in-band commands.

## Managing the Channel

```bash
astonish channels status           # Check channel status
astonish channels disable email    # Disable the channel
```
