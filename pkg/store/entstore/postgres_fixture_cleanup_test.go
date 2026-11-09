//go:build integration

package entstore

import "testing"

// Store.Close owns platform pools, whereas each routed org owns its tenant
// pools. Fixtures must retire both before reopening or dropping their databases.
// Call only after the fixture's workers have joined. Deleting entries makes a
// later registered cleanup safe after an explicit reopen has already closed it.
func closePostgresFixtureStore(t *testing.T, s *Store) {
	t.Helper()
	s.orgClients.Range(func(key, value any) bool {
		if err := value.(*orgDataStore).Close(); err != nil {
			t.Errorf("close PostgreSQL fixture org %v: %v", key, err)
		}
		s.orgClients.Delete(key)
		return true
	})
	if err := s.Close(); err != nil {
		t.Errorf("close PostgreSQL fixture platform: %v", err)
	}
}
