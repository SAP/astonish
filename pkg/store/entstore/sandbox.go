package entstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	platforment "github.com/SAP/astonish/ent/platform"
	"github.com/SAP/astonish/ent/platform/sandboxlayer"
	"github.com/SAP/astonish/ent/platform/sandboxtemplate"
	"github.com/SAP/astonish/ent/platform/schema"
	"github.com/SAP/astonish/pkg/store"
)

// --------------------------------------------------------------------------
// LayerStore implementation
// --------------------------------------------------------------------------

// layerStore implements store.LayerStore using the Ent platform client.
type layerStore struct {
	client *platforment.Client
}

func (ls *layerStore) PutLayer(ctx context.Context, layer *store.SandboxLayer) error {
	// Idempotent: if the layer already exists, succeed without modifying it.
	existing, err := ls.client.SandboxLayer.Get(ctx, layer.LayerID)
	if err != nil && !platforment.IsNotFound(err) {
		return fmt.Errorf("check existing layer: %w", err)
	}
	if existing != nil {
		return nil // already exists — dedup
	}

	create := ls.client.SandboxLayer.Create().
		SetID(layer.LayerID).
		SetCephfsPath(layer.CephFSPath).
		SetSizeBytes(layer.SizeBytes).
		SetRefCount(layer.RefCount).
		SetNillableParentLayer(layer.ParentLayer).
		SetNillableCreatedBy(nilIfEmpty(layer.CreatedBy))

	if !layer.AddedAt.IsZero() {
		create.SetAddedAt(layer.AddedAt)
	}
	if !layer.LastReferenced.IsZero() {
		create.SetLastReferenced(layer.LastReferenced)
	}

	_, err = create.Save(ctx)
	if err != nil {
		// Handle race condition: another goroutine may have inserted concurrently.
		if platforment.IsConstraintError(err) {
			return nil
		}
		return err
	}
	return nil
}

func (ls *layerStore) GetLayer(ctx context.Context, layerID string) (*store.SandboxLayer, error) {
	ent, err := ls.client.SandboxLayer.Get(ctx, layerID)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return entLayerToStore(ent), nil
}

func (ls *layerStore) IncrementRefCount(ctx context.Context, layerID string) error {
	n, err := ls.client.SandboxLayer.Update().
		Where(sandboxlayer.IDEQ(layerID)).
		AddRefCount(1).
		SetLastReferenced(time.Now()).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("layer %q not found", layerID)
	}
	return nil
}

func (ls *layerStore) DecrementRefCount(ctx context.Context, layerID string) error {
	// First get the current layer to check ref_count.
	ent, err := ls.client.SandboxLayer.Get(ctx, layerID)
	if err != nil {
		if platforment.IsNotFound(err) {
			return fmt.Errorf("layer %q not found", layerID)
		}
		return err
	}
	if ent.RefCount <= 0 {
		return fmt.Errorf("layer %q ref_count is already 0", layerID)
	}

	return ls.client.SandboxLayer.UpdateOneID(layerID).
		AddRefCount(-1).
		Exec(ctx)
}

func (ls *layerStore) ListUnreferenced(ctx context.Context, grace time.Duration) ([]*store.SandboxLayer, error) {
	cutoff := time.Now().Add(-grace)
	ents, err := ls.client.SandboxLayer.Query().
		Where(
			sandboxlayer.RefCountEQ(0),
			sandboxlayer.AddedAtLT(cutoff),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}

	layers := make([]*store.SandboxLayer, len(ents))
	for i, e := range ents {
		layers[i] = entLayerToStore(e)
	}
	return layers, nil
}

func (ls *layerStore) ListAll(ctx context.Context) ([]*store.SandboxLayer, error) {
	ents, err := ls.client.SandboxLayer.Query().
		Order(sandboxlayer.ByAddedAt()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	layers := make([]*store.SandboxLayer, len(ents))
	for i, e := range ents {
		layers[i] = entLayerToStore(e)
	}
	return layers, nil
}

func (ls *layerStore) DeleteLayer(ctx context.Context, layerID string) error {
	// Check ref_count > 0 before deleting.
	ent, err := ls.client.SandboxLayer.Get(ctx, layerID)
	if err != nil {
		if platforment.IsNotFound(err) {
			return fmt.Errorf("layer %q not found", layerID)
		}
		return err
	}
	if ent.RefCount > 0 {
		return fmt.Errorf("layer %q has ref_count=%d, cannot delete", layerID, ent.RefCount)
	}

	return ls.client.SandboxLayer.DeleteOneID(layerID).Exec(ctx)
}

func entLayerToStore(e *platforment.SandboxLayer) *store.SandboxLayer {
	l := &store.SandboxLayer{
		LayerID:        e.ID,
		ParentLayer:    e.ParentLayer,
		CephFSPath:     e.CephfsPath,
		SizeBytes:      e.SizeBytes,
		RefCount:       e.RefCount,
		AddedAt:        e.AddedAt,
		LastReferenced: e.LastReferenced,
	}
	if e.CreatedBy != nil {
		l.CreatedBy = *e.CreatedBy
	}
	return l
}

// Compile-time assertion.
var _ store.LayerStore = (*layerStore)(nil)

// --------------------------------------------------------------------------
// SandboxTemplateStore implementation
// --------------------------------------------------------------------------

// sandboxTemplateStore implements store.SandboxTemplateStore using the Ent platform client.
type sandboxTemplateStore struct {
	client *platforment.Client

	// In-process build lock (for simplicity + portability across PG/SQLite).
	buildMu   sync.Mutex
	buildHeld bool
}

func (ts *sandboxTemplateStore) Create(ctx context.Context, tpl *store.SandboxTemplate) error {
	var id uuid.UUID
	if tpl.ID == "" {
		id = uuid.New()
		tpl.ID = id.String()
	} else {
		var err error
		id, err = uuid.Parse(tpl.ID)
		if err != nil {
			return fmt.Errorf("invalid template ID: %w", err)
		}
	}

	create := ts.client.SandboxTemplate.Create().
		SetID(id).
		SetSlug(tpl.Slug).
		SetScope(sandboxtemplate.Scope(tpl.Scope)).
		SetOwnerID(tpl.OwnerID).
		SetPurpose(string(tpl.Purpose)).
		SetName(tpl.Name).
		SetDescription(tpl.Description).
		SetVersion(tpl.Version)

	if tpl.ParentTemplateID != nil {
		parentID, err := uuid.Parse(*tpl.ParentTemplateID)
		if err != nil {
			return fmt.Errorf("invalid parent_template_id: %w", err)
		}
		create.SetParentTemplateID(parentID)
	}

	if tpl.TopLayerID != nil {
		create.SetTopLayerID(*tpl.TopLayerID)
	}

	if tpl.SandboxImage != nil {
		create.SetSandboxImage(*tpl.SandboxImage)
	}

	if len(tpl.Packages) > 0 {
		create.SetPackages(tpl.Packages)
	}
	if tpl.DockerfileBody != nil {
		create.SetDockerfileBody(*tpl.DockerfileBody)
	}
	if len(tpl.BootstrapFiles) > 0 {
		create.SetBootstrapFiles(storeBootstrapToSchema(tpl.BootstrapFiles))
	}
	if tpl.BuildStatus != "" {
		create.SetBuildStatus(tpl.BuildStatus)
	}
	if tpl.BuildJobName != "" {
		create.SetBuildJobName(tpl.BuildJobName)
	}
	if tpl.LastBuiltImage != "" {
		create.SetLastBuiltImage(tpl.LastBuiltImage)
	}

	if tpl.CreatedBy != "" {
		createdBy, err := uuid.Parse(tpl.CreatedBy)
		if err != nil {
			return fmt.Errorf("invalid created_by: %w", err)
		}
		create.SetCreatedBy(createdBy)
	}

	if !tpl.CreatedAt.IsZero() {
		create.SetCreatedAt(tpl.CreatedAt)
	}
	if !tpl.UpdatedAt.IsZero() {
		create.SetUpdatedAt(tpl.UpdatedAt)
	}

	_, err := create.Save(ctx)
	return err
}

func (ts *sandboxTemplateStore) GetByID(ctx context.Context, id string) (*store.SandboxTemplate, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid template ID: %w", err)
	}

	ent, err := ts.client.SandboxTemplate.Get(ctx, uid)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return entTemplateToStore(ent), nil
}

func (ts *sandboxTemplateStore) GetBySlug(ctx context.Context, scope store.SandboxTemplateScope, ownerID, slug string) (*store.SandboxTemplate, error) {
	q := ts.client.SandboxTemplate.Query().
		Where(
			sandboxtemplate.ScopeEQ(sandboxtemplate.Scope(scope)),
			sandboxtemplate.SlugEQ(slug),
		)

	// ownerID is ignored for global scope.
	if scope != store.SandboxTemplateScopeGlobal {
		q = q.Where(sandboxtemplate.OwnerIDEQ(ownerID))
	}

	ent, err := q.Only(ctx)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return entTemplateToStore(ent), nil
}

func (ts *sandboxTemplateStore) List(ctx context.Context, filter store.SandboxTemplateFilter) ([]*store.SandboxTemplate, error) {
	q := ts.client.SandboxTemplate.Query()

	if filter.Scope != "" {
		q = q.Where(sandboxtemplate.ScopeEQ(sandboxtemplate.Scope(filter.Scope)))
	}
	if filter.OwnerID != "" {
		q = q.Where(sandboxtemplate.OwnerIDEQ(filter.OwnerID))
	}
	if filter.Purpose != "" {
		q = q.Where(sandboxtemplate.PurposeEQ(string(filter.Purpose)))
	}

	ents, err := q.
		Order(sandboxtemplate.ByScope(), sandboxtemplate.BySlug()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	templates := make([]*store.SandboxTemplate, len(ents))
	for i, e := range ents {
		templates[i] = entTemplateToStore(e)
	}
	return templates, nil
}

func (ts *sandboxTemplateStore) Update(ctx context.Context, tpl *store.SandboxTemplate) error {
	uid, err := uuid.Parse(tpl.ID)
	if err != nil {
		return fmt.Errorf("invalid template ID: %w", err)
	}

	update := ts.client.SandboxTemplate.UpdateOneID(uid).
		SetName(tpl.Name).
		SetDescription(tpl.Description).
		SetPurpose(string(tpl.Purpose)).
		SetVersion(tpl.Version).
		SetUpdatedAt(time.Now())

	if tpl.TopLayerID != nil {
		update.SetTopLayerID(*tpl.TopLayerID)
	} else {
		update.ClearTopLayerID()
	}

	if tpl.SandboxImage != nil {
		update.SetSandboxImage(*tpl.SandboxImage)
	} else {
		update.ClearSandboxImage()
	}

	// Build-related fields.
	if tpl.Packages != nil {
		update.SetPackages(tpl.Packages)
	}
	if tpl.DockerfileBody != nil {
		update.SetDockerfileBody(*tpl.DockerfileBody)
	} else {
		update.ClearDockerfileBody()
	}
	if tpl.BootstrapFiles != nil {
		update.SetBootstrapFiles(storeBootstrapToSchema(tpl.BootstrapFiles))
	}
	update.SetBuildStatus(tpl.BuildStatus)
	update.SetBuildJobName(tpl.BuildJobName)
	update.SetBuildError(tpl.BuildError)
	update.SetLastBuiltImage(tpl.LastBuiltImage)
	if tpl.BuildStartedAt != nil {
		update.SetBuildStartedAt(*tpl.BuildStartedAt)
	} else {
		update.ClearBuildStartedAt()
	}

	return update.Exec(ctx)
}

func (ts *sandboxTemplateStore) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("invalid template ID: %w", err)
	}
	return ts.client.SandboxTemplate.DeleteOneID(uid).Exec(ctx)
}

func (ts *sandboxTemplateStore) Resolve(ctx context.Context, id string) (*store.ResolvedTemplateChain, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("invalid template ID: %w", err)
	}

	// Walk the parent chain collecting layer IDs.
	var layerIDs []string
	seen := make(map[uuid.UUID]bool)
	currentID := uid

	for {
		if seen[currentID] {
			return nil, fmt.Errorf("cycle detected in template chain at %s", currentID)
		}
		seen[currentID] = true

		tpl, err := ts.client.SandboxTemplate.Get(ctx, currentID)
		if err != nil {
			if platforment.IsNotFound(err) {
				return nil, fmt.Errorf("template %s not found in chain", currentID)
			}
			return nil, err
		}

		if tpl.TopLayerID != nil {
			layerIDs = append(layerIDs, *tpl.TopLayerID)
		}

		if tpl.ParentTemplateID == nil {
			break
		}
		currentID = *tpl.ParentTemplateID
	}

	// Reverse: we collected from leaf to root, but the interface says oldest first.
	for i, j := 0, len(layerIDs)-1; i < j; i, j = i+1, j-1 {
		layerIDs[i], layerIDs[j] = layerIDs[j], layerIDs[i]
	}

	return &store.ResolvedTemplateChain{
		TemplateID: id,
		LayerIDs:   layerIDs,
	}, nil
}

func (ts *sandboxTemplateStore) ListRoots(ctx context.Context) ([]*store.SandboxTemplate, error) {
	ents, err := ts.client.SandboxTemplate.Query().
		Where(sandboxtemplate.ParentTemplateIDIsNil()).
		Order(sandboxtemplate.BySlug()).
		All(ctx)
	if err != nil {
		return nil, err
	}

	templates := make([]*store.SandboxTemplate, len(ents))
	for i, e := range ents {
		templates[i] = entTemplateToStore(e)
	}
	return templates, nil
}

func (ts *sandboxTemplateStore) GetBaseConfig(ctx context.Context) (*store.BaseConfigInfo, error) {
	ent, err := ts.client.SandboxTemplate.Query().
		Where(
			sandboxtemplate.SlugEQ("base"),
			sandboxtemplate.ScopeEQ(sandboxtemplate.ScopeGlobal),
		).
		Only(ctx)
	if err != nil {
		if platforment.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	info := &store.BaseConfigInfo{
		UpdatedAt: ent.UpdatedAt,
	}

	if ent.TopLayerID != nil {
		info.LayerID = *ent.TopLayerID
	}

	if ent.BaseConfig != nil {
		configJSON, err := json.Marshal(ent.BaseConfig)
		if err != nil {
			return nil, fmt.Errorf("marshal base_config: %w", err)
		}
		info.ConfigJSON = configJSON
	}

	if ent.ConfiguredBy != nil {
		info.ConfiguredBy = ent.ConfiguredBy.String()
	}

	info.ConfiguredAt = ent.ConfiguredAt

	// Get size from the layer if present.
	if ent.TopLayerID != nil {
		layer, err := ts.client.SandboxLayer.Get(ctx, *ent.TopLayerID)
		if err == nil {
			info.SizeBytes = layer.SizeBytes
		}
	}

	return info, nil
}

func (ts *sandboxTemplateStore) SetBaseConfig(ctx context.Context, newLayerID string, configJSON []byte, configuredBy string) error {
	// Find the base template.
	base, err := ts.client.SandboxTemplate.Query().
		Where(
			sandboxtemplate.SlugEQ("base"),
			sandboxtemplate.ScopeEQ(sandboxtemplate.ScopeGlobal),
		).
		Only(ctx)
	if err != nil {
		return fmt.Errorf("get base template: %w", err)
	}

	now := time.Now()
	update := base.Update().
		SetTopLayerID(newLayerID).
		SetConfiguredAt(now).
		SetUpdatedAt(now)

	if configJSON != nil {
		var configMap map[string]interface{}
		if err := json.Unmarshal(configJSON, &configMap); err != nil {
			return fmt.Errorf("unmarshal config JSON: %w", err)
		}
		update.SetBaseConfig(configMap)
	}

	if configuredBy != "" {
		cbUUID, err := uuid.Parse(configuredBy)
		if err != nil {
			return fmt.Errorf("invalid configured_by: %w", err)
		}
		update.SetConfiguredBy(cbUUID)
	}

	return update.Exec(ctx)
}

func (ts *sandboxTemplateStore) GetBaseTopLayerID(ctx context.Context) (string, error) {
	ent, err := ts.client.SandboxTemplate.Query().
		Where(
			sandboxtemplate.SlugEQ("base"),
			sandboxtemplate.ScopeEQ(sandboxtemplate.ScopeGlobal),
		).
		Select(sandboxtemplate.FieldTopLayerID).
		Only(ctx)
	if err != nil {
		if platforment.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	if ent.TopLayerID == nil {
		return "", nil
	}
	return *ent.TopLayerID, nil
}

func (ts *sandboxTemplateStore) AcquireBuildLock(ctx context.Context) (bool, func(), error) {
	ts.buildMu.Lock()
	defer ts.buildMu.Unlock()

	// Check DB-level lock via platform_settings row.
	const lockKey = "build_lock"
	setting, err := ts.client.PlatformSetting.Get(ctx, lockKey)
	if err != nil && !platforment.IsNotFound(err) {
		return false, nil, fmt.Errorf("check build lock: %w", err)
	}

	now := time.Now()
	staleThreshold := now.Add(-90 * time.Minute)

	if setting != nil {
		// Check if lock is held and not stale.
		if tsVal, ok := setting.Value["timestamp"].(string); ok {
			lockTime, parseErr := time.Parse(time.RFC3339, tsVal)
			if parseErr == nil && lockTime.After(staleThreshold) {
				// Lock is held and not stale.
				return false, nil, nil
			}
		}
	}

	// Acquire the lock: upsert the platform_settings row.
	lockValue := map[string]interface{}{
		"holder":    fmt.Sprintf("pod-%d", now.UnixNano()),
		"timestamp": now.Format(time.RFC3339),
	}

	if setting == nil {
		// Create the lock row.
		_, err = ts.client.PlatformSetting.Create().
			SetID(lockKey).
			SetValue(lockValue).
			SetUpdatedAt(now).
			Save(ctx)
	} else {
		// Update the existing lock row.
		err = ts.client.PlatformSetting.UpdateOneID(lockKey).
			SetValue(lockValue).
			SetUpdatedAt(now).
			Exec(ctx)
	}
	if err != nil {
		return false, nil, fmt.Errorf("acquire build lock: %w", err)
	}

	ts.buildHeld = true

	release := func() {
		ts.buildMu.Lock()
		defer ts.buildMu.Unlock()

		ts.buildHeld = false

		// Clear the lock row value.
		clearValue := map[string]interface{}{
			"holder":    "",
			"timestamp": "",
		}
		_ = ts.client.PlatformSetting.UpdateOneID(lockKey).
			SetValue(clearValue).
			SetUpdatedAt(time.Now()).
			Exec(ctx)
	}

	return true, release, nil
}

func (ts *sandboxTemplateStore) IsBuildInProgress(ctx context.Context) (bool, error) {
	ts.buildMu.Lock()
	held := ts.buildHeld
	ts.buildMu.Unlock()
	if held {
		return true, nil
	}

	const lockKey = "build_lock"
	setting, err := ts.client.PlatformSetting.Get(ctx, lockKey)
	if err != nil {
		if platforment.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	now := time.Now()
	staleThreshold := now.Add(-90 * time.Minute)

	tsStr, ok := setting.Value["timestamp"].(string)
	if !ok || tsStr == "" {
		return false, nil
	}

	lockTime, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return false, nil
	}

	return lockTime.After(staleThreshold), nil
}

func (ts *sandboxTemplateStore) AcquireTemplateBuildLock(ctx context.Context, templateID string) (bool, func(), error) {
	ts.buildMu.Lock()
	defer ts.buildMu.Unlock()

	lockKey := "build_lock:" + templateID
	setting, err := ts.client.PlatformSetting.Get(ctx, lockKey)
	if err != nil && !platforment.IsNotFound(err) {
		return false, nil, fmt.Errorf("check template build lock: %w", err)
	}

	now := time.Now()
	staleThreshold := now.Add(-10 * time.Minute) // image builds can take longer

	if setting != nil {
		if tsVal, ok := setting.Value["timestamp"].(string); ok {
			lockTime, parseErr := time.Parse(time.RFC3339, tsVal)
			if parseErr == nil && lockTime.After(staleThreshold) {
				return false, nil, nil
			}
		}
	}

	lockValue := map[string]interface{}{
		"holder":    fmt.Sprintf("pod-%d", now.UnixNano()),
		"timestamp": now.Format(time.RFC3339),
	}

	if setting == nil {
		_, err = ts.client.PlatformSetting.Create().
			SetID(lockKey).
			SetValue(lockValue).
			SetUpdatedAt(now).
			Save(ctx)
	} else {
		err = ts.client.PlatformSetting.UpdateOneID(lockKey).
			SetValue(lockValue).
			SetUpdatedAt(now).
			Exec(ctx)
	}
	if err != nil {
		return false, nil, fmt.Errorf("acquire template build lock: %w", err)
	}

	release := func() {
		ts.buildMu.Lock()
		defer ts.buildMu.Unlock()

		clearValue := map[string]interface{}{
			"holder":    "",
			"timestamp": "",
		}
		_ = ts.client.PlatformSetting.UpdateOneID(lockKey).
			SetValue(clearValue).
			SetUpdatedAt(time.Now()).
			Exec(ctx)
	}

	return true, release, nil
}

func (ts *sandboxTemplateStore) IsTemplateBuildInProgress(ctx context.Context, templateID string) (bool, error) {
	lockKey := "build_lock:" + templateID
	setting, err := ts.client.PlatformSetting.Get(ctx, lockKey)
	if err != nil {
		if platforment.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	now := time.Now()
	staleThreshold := now.Add(-10 * time.Minute)

	tsStr, ok := setting.Value["timestamp"].(string)
	if !ok || tsStr == "" {
		return false, nil
	}

	lockTime, err := time.Parse(time.RFC3339, tsStr)
	if err != nil {
		return false, nil
	}

	return lockTime.After(staleThreshold), nil
}

func entTemplateToStore(e *platforment.SandboxTemplate) *store.SandboxTemplate {
	tpl := &store.SandboxTemplate{
		ID:             e.ID.String(),
		Slug:           e.Slug,
		Scope:          store.SandboxTemplateScope(e.Scope),
		OwnerID:        e.OwnerID,
		Purpose:        store.SandboxTemplatePurpose(e.Purpose),
		Name:           e.Name,
		Description:    e.Description,
		TopLayerID:     e.TopLayerID,
		SandboxImage:   e.SandboxImage,
		Packages:       e.Packages,
		DockerfileBody: e.DockerfileBody,
		BootstrapFiles: schemaBootstrapToStore(e.BootstrapFiles),
		BuildStatus:    e.BuildStatus,
		BuildJobName:   e.BuildJobName,
		BuildError:     e.BuildError,
		LastBuiltImage: e.LastBuiltImage,
		BuildStartedAt: e.BuildStartedAt,
		Version:        e.Version,
		CreatedAt:      e.CreatedAt,
		UpdatedAt:      e.UpdatedAt,
	}

	if e.ParentTemplateID != nil {
		s := e.ParentTemplateID.String()
		tpl.ParentTemplateID = &s
	}

	if e.CreatedBy != nil {
		tpl.CreatedBy = e.CreatedBy.String()
	}

	return tpl
}

func storeBootstrapToSchema(files []store.BootstrapFile) []schema.BootstrapFileSchema {
	if len(files) == 0 {
		return nil
	}
	out := make([]schema.BootstrapFileSchema, 0, len(files))
	for _, f := range files {
		out = append(out, schema.BootstrapFileSchema{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}
	return out
}

func schemaBootstrapToStore(files []schema.BootstrapFileSchema) []store.BootstrapFile {
	if len(files) == 0 {
		return nil
	}
	out := make([]store.BootstrapFile, 0, len(files))
	for _, f := range files {
		out = append(out, store.BootstrapFile{
			Path:    f.Path,
			Content: f.Content,
			Mode:    f.Mode,
		})
	}
	return out
}

// nilIfEmpty returns nil if s is empty, otherwise returns a pointer to s.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Compile-time assertions.
var _ store.LayerStore = (*layerStore)(nil)
var _ store.SandboxTemplateStore = (*sandboxTemplateStore)(nil)
