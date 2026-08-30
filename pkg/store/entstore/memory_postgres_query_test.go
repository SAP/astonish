package entstore

import (
	"os"
	"strings"
	"testing"
)

func TestPostgresVectorSearchExcludesMemoriesWithoutEmbeddings(t *testing.T) {
	files := []string{"personal_memories.go", "team_memories.go", "org_memories.go"}
	for _, file := range files {
		t.Run(file, func(t *testing.T) {
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			queries := string(body)
			guards := strings.Count(queries, "WHERE embedding IS NOT NULL")
			vectorOrders := strings.Count(queries, "ORDER BY embedding <=>")
			if guards < vectorOrders {
				t.Fatalf("PostgreSQL vector queries with embedding guard = %d, vector queries = %d", guards, vectorOrders)
			}
		})
	}
}
