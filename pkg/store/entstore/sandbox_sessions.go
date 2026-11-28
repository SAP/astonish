package entstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/sandboxsession"
	"github.com/SAP/astonish/pkg/store"
)

// teamSandboxSessionStore implements store.SandboxSessionStore using the
// Ent team client. Each team has its own database, so no schema prefix needed.
type teamSandboxSessionStore struct {
	client *teament.Client
}

func (s *teamSandboxSessionStore) Put(ctx context.Context, sess *store.SandboxSession) error {
	var binding map[string]any
	if sess.BotHomeBinding != nil {
		var err error
		binding, err = botJSONMap(sess.BotHomeBinding)
		if err != nil {
			return err
		}
	}
	// Upsert: try update first, if 0 rows affected, insert.
	n, err := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sess.SessionID)).
		SetChatSessionID(sess.ChatSessionID).
		SetBackend(sess.Backend).
		SetState(sandboxsession.State(sess.State)).
		SetPinned(sess.Pinned).
		SetUpdatedAt(time.Now()).
		SetLastActiveAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session put (update): %w", err)
	}

	if n > 0 {
		// Update optional fields on existing row.
		mut := s.client.SandboxSession.Update().Where(sandboxsession.IDEQ(sess.SessionID))
		applyOptionalFields(mut, sess)
		if binding != nil {
			mut.SetBotHomeBinding(binding)
		} else {
			mut.ClearBotHomeBinding()
		}
		if _, err := mut.Save(ctx); err != nil {
			return fmt.Errorf("sandbox session put (update optionals): %w", err)
		}
		return nil
	}

	// Insert new row.
	create := s.client.SandboxSession.Create().
		SetID(sess.SessionID).
		SetChatSessionID(sess.ChatSessionID).
		SetBackend(sess.Backend).
		SetState(sandboxsession.State(sess.State)).
		SetPinned(sess.Pinned).
		SetCreatedAt(sess.CreatedAt).
		SetUpdatedAt(time.Now()).
		SetLastActiveAt(time.Now())

	if sess.ContainerName != "" {
		create.SetContainerName(sess.ContainerName)
	}
	// template_id is required (NOT NULL) — always set it, using zero UUID as default.
	create.SetTemplateID(parseUUIDOrZero(sess.TemplateID))
	if sess.UpperLayerID != "" {
		create.SetUpperLayerID(sess.UpperLayerID)
	}
	if sess.PodName != "" {
		create.SetPodName(sess.PodName)
	}
	if sess.NodeName != "" {
		create.SetNodeName(sess.NodeName)
	}
	if sess.BaseDomain != "" {
		create.SetBaseDomain(sess.BaseDomain)
	}
	if sess.CreatedBy != "" {
		create.SetCreatedBy(parseUUIDOrZero(sess.CreatedBy))
	}
	if len(sess.ExposedPorts) > 0 {
		create.SetExposedPorts(portsToAny(sess.ExposedPorts))
	}

	if binding != nil {
		create.SetBotHomeBinding(binding)
	}
	if _, err := create.Save(ctx); err != nil {
		return fmt.Errorf("sandbox session put (insert): %w", err)
	}
	return nil
}

func (s *teamSandboxSessionStore) Get(ctx context.Context, sessionID string) (*store.SandboxSession, error) {
	row, err := s.client.SandboxSession.Get(ctx, sessionID)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sandbox session get: %w", err)
	}
	return entSandboxToStore(row), nil
}

func (s *teamSandboxSessionStore) GetByContainerName(ctx context.Context, containerName string) (*store.SandboxSession, error) {
	row, err := s.client.SandboxSession.Query().
		Where(sandboxsession.ContainerNameEQ(containerName)).
		Only(ctx)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("sandbox session get by container: %w", err)
	}
	return entSandboxToStore(row), nil
}

func (s *teamSandboxSessionStore) List(ctx context.Context, filter store.SandboxSessionFilter) ([]*store.SandboxSession, error) {
	q := s.client.SandboxSession.Query().Order(teament.Desc(sandboxsession.FieldCreatedAt))

	if filter.State != "" {
		q = q.Where(sandboxsession.StateEQ(sandboxsession.State(filter.State)))
	}
	if filter.CreatedBy != "" {
		q = q.Where(sandboxsession.CreatedByEQ(parseUUIDOrZero(filter.CreatedBy)))
	}
	if filter.Pinned != nil {
		q = q.Where(sandboxsession.PinnedEQ(*filter.Pinned))
	}
	if filter.ContainerName != "" {
		q = q.Where(sandboxsession.ContainerNameEQ(filter.ContainerName))
	}

	rows, err := q.All(ctx)
	if err != nil {
		return nil, fmt.Errorf("sandbox session list: %w", err)
	}

	result := make([]*store.SandboxSession, len(rows))
	for i, r := range rows {
		result[i] = entSandboxToStore(r)
	}
	return result, nil
}

func (s *teamSandboxSessionStore) Delete(ctx context.Context, sessionID string) error {
	_, err := s.client.SandboxSession.Delete().
		Where(sandboxsession.IDEQ(sessionID)).
		Exec(ctx)
	if err != nil && !teament.IsNotFound(err) {
		return fmt.Errorf("sandbox session delete: %w", err)
	}
	return nil
}

func (s *teamSandboxSessionStore) UpdateState(ctx context.Context, sessionID string, state store.SandboxSessionState) error {
	n, err := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sessionID)).
		SetState(sandboxsession.State(state)).
		SetUpdatedAt(time.Now()).
		SetLastActiveAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session update state: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	return nil
}

func (s *teamSandboxSessionStore) UpdatePorts(ctx context.Context, sessionID string, ports []int) error {
	n, err := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sessionID)).
		SetExposedPorts(portsToAny(ports)).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session update ports: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	return nil
}

func (s *teamSandboxSessionStore) SetBaseDomain(ctx context.Context, sessionID, baseDomain string) error {
	n, err := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sessionID)).
		SetBaseDomain(baseDomain).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session set base domain: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	return nil
}

func (s *teamSandboxSessionStore) SetPinned(ctx context.Context, sessionID string, pinned bool) error {
	n, err := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sessionID)).
		SetPinned(pinned).
		SetUpdatedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session set pinned: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	return nil
}

func (s *teamSandboxSessionStore) SetUpperLayer(ctx context.Context, sessionID, upperLayerID string) error {
	mut := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sessionID)).
		SetUpdatedAt(time.Now())
	if upperLayerID == "" {
		mut = mut.ClearUpperLayerID()
	} else {
		mut = mut.SetUpperLayerID(upperLayerID)
	}
	n, err := mut.Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session set upper layer: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("sandbox session %s not found", sessionID)
	}
	return nil
}

func (s *teamSandboxSessionStore) TouchActivity(ctx context.Context, sessionID string) error {
	n, err := s.client.SandboxSession.Update().
		Where(sandboxsession.IDEQ(sessionID)).
		SetLastActiveAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("sandbox session touch activity: %w", err)
	}
	_ = n // no-op if absent
	return nil
}

// --- Helpers ---

func entSandboxToStore(row *teament.SandboxSession) *store.SandboxSession {
	sess := &store.SandboxSession{
		SessionID:     row.ID,
		ChatSessionID: row.ChatSessionID,
		Backend:       row.Backend,
		TemplateID:    row.TemplateID.String(),
		State:         store.SandboxSessionState(row.State),
		Pinned:        row.Pinned,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
		LastActiveAt:  row.LastActiveAt,
	}
	if row.ContainerName != nil {
		sess.ContainerName = *row.ContainerName
	}
	if row.UpperLayerID != nil {
		sess.UpperLayerID = *row.UpperLayerID
	}
	if row.PodName != nil {
		sess.PodName = *row.PodName
	}
	if row.NodeName != nil {
		sess.NodeName = *row.NodeName
	}
	if row.BaseDomain != nil {
		sess.BaseDomain = *row.BaseDomain
	}
	if row.CreatedBy != nil {
		sess.CreatedBy = row.CreatedBy.String()
	}
	sess.ExposedPorts = anyToPorts(row.ExposedPorts)
	if row.BotHomeBinding != nil {
		sess.BotHomeBinding = new(store.BotHomeBinding)
		// Invalid registry metadata stays visibly linked but unusable; it must
		// never be treated as an ordinary session or execution authority.
		if err := botDecode(row.BotHomeBinding, sess.BotHomeBinding); err != nil {
			*sess.BotHomeBinding = store.BotHomeBinding{}
		}
	}
	return sess
}

func portsToAny(ports []int) []any {
	result := make([]any, len(ports))
	for i, p := range ports {
		result[i] = p
	}
	return result
}

func anyToPorts(a []any) []int {
	if len(a) == 0 {
		return nil
	}
	result := make([]int, 0, len(a))
	for _, v := range a {
		switch p := v.(type) {
		case float64:
			result = append(result, int(p))
		case int:
			result = append(result, p)
		case int64:
			result = append(result, int(p))
		}
	}
	return result
}

func applyOptionalFields(mut *teament.SandboxSessionUpdate, sess *store.SandboxSession) {
	if sess.ContainerName != "" {
		mut.SetContainerName(sess.ContainerName)
	}
	if sess.TemplateID != "" {
		mut.SetTemplateID(parseUUIDOrZero(sess.TemplateID))
	}
	if sess.UpperLayerID != "" {
		mut.SetUpperLayerID(sess.UpperLayerID)
	} else {
		mut.ClearUpperLayerID()
	}
	if sess.PodName != "" {
		mut.SetPodName(sess.PodName)
	}
	if sess.NodeName != "" {
		mut.SetNodeName(sess.NodeName)
	}
	if sess.BaseDomain != "" {
		mut.SetBaseDomain(sess.BaseDomain)
	}
	if sess.CreatedBy != "" {
		mut.SetCreatedBy(parseUUIDOrZero(sess.CreatedBy))
	}
	if len(sess.ExposedPorts) > 0 {
		mut.SetExposedPorts(portsToAny(sess.ExposedPorts))
	}
}

func parseUUIDOrZero(s string) uuid.UUID {
	u, err := uuid.Parse(s)
	if err != nil {
		return uuid.UUID{}
	}
	return u
}
