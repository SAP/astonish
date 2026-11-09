package entstore

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	platforment "github.com/SAP/astonish/ent/platform"
	platformuser "github.com/SAP/astonish/ent/platform/user"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.FirstUserBootstrap = (*Store)(nil)

// BootstrapFirstUser publishes the platform directory last. Background workers
// enumerate that directory, so they cannot migrate a half-provisioned tenant.
func (s *Store) BootstrapFirstUser(ctx context.Context, u *store.User, org *store.Organization) error {
	for attempt := 0; ; attempt++ {
		err := s.bootstrapFirstUser(ctx, u, org)
		var busy interface{ Code() int }
		if s.dialect != DialectSQLite || !errors.As(err, &busy) || busy.Code()&255 != 5 || attempt == 4 {
			return err
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) bootstrapFirstUser(ctx context.Context, u *store.User, org *store.Organization) error {
	tx, err := s.platformDB.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d := dialect.SQLite
	if s.dialect == DialectPostgres {
		d = dialect.Postgres
	}
	client := platforment.NewClient(platforment.Driver(entsql.NewDriver(d, entsql.Conn{ExecQuerier: tx})))
	// Take the writer lock BEFORE any read snapshot (avoids BUSY_SNAPSHOT 517).
	// PostgreSQL's transaction advisory lock serializes across daemon instances.
	if s.dialect == DialectPostgres {
		if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(723904218)"); err != nil {
			return err
		}
	} else {
		if _, err = client.User.Update().Where(platformuser.IDEQ(uuid.Nil)).SetStatus(platformuser.StatusActive).Save(ctx); err != nil {
			return err
		}
	}
	orgs := &orgStore{client: client}
	n, err := orgs.Count(ctx)
	if err != nil {
		return err
	}
	if n != 0 {
		return store.ErrPlatformInitialized
	}
	if err = s.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		return err
	}
	data, err := s.ForOrg(org.Slug)
	if err != nil {
		return err
	}
	if err = data.ProvisionTeam(ctx, "general"); err != nil {
		return err
	}
	if err = data.ProvisionPersonalSchema(ctx, u.ID); err != nil {
		return err
	}
	orgTx, err := data.(*orgDataStore).client.Tx(ctx)
	if err != nil {
		return err
	}
	defer orgTx.Rollback()
	teams := &orgTeamStore{client: orgTx.Client()}
	team, err := teams.GetTeamBySlug(ctx, "general")
	if err != nil {
		return err
	}
	if team == nil {
		team = &store.Team{ID: uuid.NewString(), Name: "General", Slug: "general", SchemaName: "team_general"}
		if err = teams.CreateTeam(ctx, team); err != nil {
			return err
		}
	}
	member, err := teams.IsTeamMember(ctx, u.ID, "general")
	if err != nil {
		return err
	}
	if !member {
		if err = teams.AddMember(ctx, &store.TeamMembership{UserID: u.ID, TeamID: team.ID, Role: "admin", JoinedAt: time.Now()}); err != nil {
			return err
		}
	}
	first := *u
	first.PlatformRole = "superadmin"
	if err = (&userStore{client: client}).Create(ctx, &first); err != nil {
		return err
	}
	if err = orgs.Create(ctx, org); err != nil {
		return err
	}
	if err = orgs.AddMember(ctx, u.ID, org.ID, "owner"); err != nil {
		return err
	}
	if err = orgTx.Commit(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	u.PlatformRole = first.PlatformRole
	return nil
}
