# Troubleshooting

Common issues organized by category with symptoms, causes, and solutions.

## Installation

### Binary not found after install

**Symptom:** `astonish: command not found`

**Solution:** Ensure the install directory is in your `PATH`:
```bash
export PATH="$PATH:$HOME/.local/bin"
```

Add this to your shell profile (`~/.bashrc`, `~/.zshrc`) for persistence.

### Build fails with Go version error

**Symptom:** `requires go >= 1.24`

**Solution:** Update Go to 1.24.4 or later. Download from [go.dev/dl](https://go.dev/dl/).

## Provider Connectivity

### API key rejected

**Symptom:** `401 Unauthorized` or `Invalid API key`

**Solution:**
1. Verify the key in `astonish status`
2. Re-enter with `astonish setup` or edit `~/.config/astonish/config.yaml`
3. Check the key hasn't expired or been revoked in your provider dashboard

### Timeout errors

**Symptom:** `context deadline exceeded` or `request timed out`

**Solution:**
- Check network connectivity to the provider's API
- If behind a corporate proxy, configure `HTTP_PROXY`/`HTTPS_PROXY`
- Increase timeout in config: `providers.openai.timeout: "120s"`

### Model not available

**Symptom:** `model not found` or `model access denied`

**Solution:** Verify your API plan includes the requested model. Some models require specific tier access.

## Daemon

### Daemon won't start

**Symptom:** `astonish daemon start` exits immediately

**Solution:**
```bash
# Check for port conflicts
astonish daemon logs

# Kill stale process
astonish daemon stop --force
astonish daemon start
```

### Channels not connecting

**Symptom:** Daemon running but channel shows `disconnected`

**Solution:**
- Verify channel credentials in config
- For Telegram: ensure bot token is valid (`/getMe` test)
- For Email: test IMAP/SMTP credentials with another client
- For Slack: verify app tokens and event subscriptions

## Cloud Deployment

### PostgreSQL connection failed

**Symptom:** `failed to connect to database` or `connection refused`

**Solution:**
1. Verify PostgreSQL is running: `pg_isready -h localhost -p 5432`
2. Check DSN in config matches actual database credentials
3. Ensure the database exists: `psql -l | grep astonish`
4. Check `pg_hba.conf` allows connections from the Astonish host

### Migration errors

**Symptom:** `migration failed` or schema mismatch

**Solution:**
```bash
# Check current migration state
astonish platform migrate status

# Retry migrations
astonish platform migrate
```

If a migration is stuck, check PostgreSQL logs for lock contention or permission issues.

### Authentication failures

**Symptom:** `invalid credentials` or `token expired` in Studio login

**Solution:**
- Clear browser cookies and retry
- Re-authenticate: `astonish login`
- Check that the user exists in the org: `astonish platform org members --org <name>`

### Org membership issues

**Symptom:** `access denied` or content not visible

**Solution:**
- Verify membership: `astonish platform org members --org <name>`
- Check team assignment: user must be in a team to access team-scoped resources
- Ensure RLS policies are applied: `astonish platform migrate status`

## Channels

### Telegram bot not responding

**Solution:**
1. Confirm daemon is running: `astonish daemon status`
2. Check allowlist includes your user ID
3. Verify bot token: `curl https://api.telegram.org/bot<TOKEN>/getMe`
4. Check daemon logs for errors: `astonish daemon logs --follow`

### Email messages not processed

**Solution:**
1. Verify IMAP credentials connect successfully
2. Check `poll_interval` isn't set too high
3. Confirm sender is in the allowlist (check exact address match)
4. Look for parsing errors in daemon logs

## Slides

### Presentation does not open in presenter mode

Allow pop-ups and full-screen access for the Studio site, or use the in-Studio **Full screen** view.

### Template import or export fails

Check that imported templates are valid `.pptx` files no larger than 75 MiB. PDF rendering in platform deployments also requires browser support in the configured sandbox. See [Presenting and Exporting Slides](../slides/presenting-and-exporting.md) for format guidance and Slides-specific solutions.

## Browser Automation

### Browser tool fails to launch

**Symptom:** `failed to launch browser` or `chrome not found`

**Solution:**
- Install Chrome/Chromium: `apt install chromium-browser` or equivalent
- Set custom path in settings if Chrome is installed in a non-standard location
- For containers/CI: ensure `--no-sandbox` flag is enabled in browser settings

### Page load timeouts

**Symptom:** `navigation timeout` errors

**Solution:**
- Increase timeout in Studio Settings → Browser
- Check that the target URL is accessible from the machine running Astonish
- For authenticated pages, ensure credentials are configured
