package entstore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// hookHTTPClient has no ambient proxy, cookies, credentials or redirects. DNS is
// checked at connection time and the checked address is dialed (no rebind gap).
func hookHTTPClient() *http.Client {
	transport := &http.Transport{DisableKeepAlives: true, DisableCompression: true, MaxResponseHeaderBytes: 8192}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, store.ErrBotForbidden
		}
		for _, ip := range ips {
			if !hookPublicIP(ip.IP) {
				return nil, store.ErrBotForbidden
			}
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return store.ErrBotForbidden }}
}
func hookPublicIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "2001::/32"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
func fetchHook(ctx context.Context, p *store.BotHookPoll, client *http.Client) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, store.ErrBotForbidden
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, store.ErrBotInvalid
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil || len(data) > 65536 {
		return nil, store.ErrBotInvalid
	}
	return data, nil
}

type hookClientKey struct{}

func hookClientForContext(ctx context.Context) *http.Client {
	if client, ok := ctx.Value(hookClientKey{}).(*http.Client); ok {
		return client
	}
	return hookHTTPClient()
}

func (r *botRepository) prepareHookDryRun(ctx context.Context, q store.BotTriggerChange, proof *store.BotExternalEvent) (context.Context, *store.BotExternalEvent, error) {
	b, err := r.Get(ctx, q.BotID)
	if err != nil {
		return ctx, nil, err
	}
	v, err := r.db.ReadVersion(ctx, q.BotID, b.Version)
	if err != nil {
		return ctx, nil, err
	}
	for _, tr := range v.Triggers {
		if tr.ID != q.TriggerID || tr.Poll == nil {
			continue
		}
		if tr.Enabled || tr.HookStatus != store.HookDisabled || b.Revision != q.ExpectedRevision {
			return ctx, nil, store.ErrBotConflict
		}
		data, err := fetchHook(ctx, tr.Poll, hookClientForContext(ctx))
		if err != nil {
			return ctx, nil, err
		}
		resolved, ok := ctx.Value(triggerCredentialContextKey{}).(resolvedTriggerCredential)
		if !ok {
			return ctx, nil, store.ErrBotForbidden
		}
		event := &store.BotExternalEvent{BotID: q.BotID, TriggerID: q.TriggerID, Version: b.Version, EventID: uuid.NewString(), Timestamp: r.clock().Unix(), Payload: data}
		event.Signature = store.BotTriggerSignature(resolved.secret, *event, "dry-run")
		return ctx, event, nil
	}
	return ctx, proof, nil
}

type hookOccurrenceKey struct{}

func (s *personalBotStore) PollScheduledHook(ctx context.Context, q store.BotScheduledOccurrence) (bool, *store.BotScheduledAdmission, error) {
	return s.repository().pollScheduledHook(store.WithServices(ctx, &store.Services{PersonalCredentials: s.credentials}), q, hookHTTPClient())
}
func (s *teamBotStore) PollScheduledHook(ctx context.Context, q store.BotScheduledOccurrence) (bool, *store.BotScheduledAdmission, error) {
	return s.repository().pollScheduledHook(store.WithServices(ctx, &store.Services{Credentials: s.credentials}), q, hookHTTPClient())
}

func (r *botRepository) pollScheduledHook(ctx context.Context, q store.BotScheduledOccurrence, client *http.Client) (bool, *store.BotScheduledAdmission, error) {
	b, err := r.Get(ctx, q.BotID)
	if err != nil {
		return true, nil, err
	}
	v, err := r.db.ReadVersion(ctx, q.BotID, b.Version)
	if err != nil {
		return true, nil, err
	}
	for _, tr := range v.Triggers {
		if tr.ID != q.TriggerID || tr.Kind != store.TriggerHook || tr.Poll == nil {
			continue
		}
		ctx, err = r.resolveTriggerCredential(ctx, q.BotID, q.TriggerID)
		if err != nil {
			return true, nil, err
		}
		rec, err := r.db.GetBot(ctx, q.BotID)
		if err != nil {
			return true, nil, err
		}
		if err := r.validateHookOccurrence(ctx, r.db, rec, tr, q); err != nil {
			return true, nil, err
		}
		data, err := fetchHook(ctx, tr.Poll, client)
		if err != nil {
			return true, nil, err
		}
		watermark := fmt.Sprintf("%x", sha256.Sum256(data))
		if watermark == tr.HookWatermark {
			return true, nil, nil
		}
		// Content watermark is durable through the existing run idempotency index;
		// unchanged content (including a return to previously seen content) never queues twice.
		event := store.BotExternalEvent{BotID: q.BotID, TriggerID: q.TriggerID, Version: q.Version, EventID: "poll:" + watermark, Timestamp: r.clock().Unix(), Payload: data}
		resolved := ctx.Value(triggerCredentialContextKey{}).(resolvedTriggerCredential)
		event.Signature = store.BotTriggerSignature(resolved.secret, event, "run")
		key := fmt.Sprintf("bot-event-v1:%x", sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%s", q.BotID, q.TriggerID, event.EventID))))
		if _, err := r.db.FindRun(ctx, q.BotID, key); err == nil {
			return true, nil, nil
		} else if err != store.ErrBotNotFound {
			return true, nil, err
		}
		ctx = context.WithValue(ctx, hookOccurrenceKey{}, q)
		run, err := r.AdmitExternalRun(ctx, event)
		if err != nil {
			return true, nil, err
		}
		return true, &store.BotScheduledAdmission{Run: *run}, nil
	}
	return false, nil, nil
}
func (r *botRepository) validateHookOccurrence(ctx context.Context, tx botDraftDB, rec *botStoredRecord, tr store.TriggerSpec, q store.BotScheduledOccurrence) error {
	if rec.Bot.Status != store.BotActive || rec.Bot.Version != q.Version || !tr.Enabled || tr.HookStatus != store.HookEnabled || tr.Fingerprint != triggerCredentialFingerprint(ctx, tr) || tr.ScheduleID == nil || tr.ScheduleID.String() != q.JobID || q.OccurrenceAt.IsZero() || q.OccurrenceAt.After(r.clock()) {
		return store.ErrBotConflict
	}
	if err := r.validateTriggerCredential(ctx, rec, tr); err != nil {
		return err
	}
	db, ok := tx.(botScheduledDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	job, err := db.readBotSchedule(ctx, *tr.ScheduleID)
	if err != nil {
		return err
	}
	identity := fmt.Sprintf("%s/%d/%s/%s", q.BotID, q.Version, q.TriggerID, q.OccurrenceAt.UTC().Format(time.RFC3339Nano))
	key := fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
	if q.IdempotencyKey != key || !job.Enabled || job.Mode != "bot_trigger" || job.OwnerID != rec.Bot.OwnerID.String() || job.Payload.BotID != q.BotID.String() || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.BotVersion != q.Version || job.NextRun == nil || !job.NextRun.Equal(q.OccurrenceAt) || job.Schedule.Cron != tr.Cron || job.Schedule.Timezone != tr.Timezone || job.Payload.Flow != "" || job.Payload.Instructions != "" || len(job.Payload.Params) != 0 {
		return store.ErrBotForbidden
	}
	return nil
}
