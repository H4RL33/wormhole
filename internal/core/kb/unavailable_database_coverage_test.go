package kb

import (
	"context"
	"encoding/json"
	"testing"
)

func TestKBStoreOperationsFailClosedWhenDatabaseIsUnavailable(t *testing.T) {
	s := testStore(t)
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := "00000000-0000-0000-0000-000000000001"
	checks := []struct {
		name string
		call func() error
	}{
		{"write", func() error {
			_, err := s.WriteArticle(ctx, id, id, "title", "body", json.RawMessage(`{}`), nil, false)
			return err
		}},
		{"ensure bootstrap", func() error {
			_, err := s.EnsureBootstrapArticle(ctx, id, id, "bootstrap", "title", "body", json.RawMessage(`{}`))
			return err
		}},
		{"write with id", func() error {
			_, err := s.WriteArticleWithID(ctx, id, id, id, "title", "body", json.RawMessage(`{}`), nil, false)
			return err
		}},
		{"search", func() error { _, err := s.SearchArticles(ctx, id, id, "query", 10); return err }},
		{"search metadata", func() error { _, _, err := s.SearchArticlesWithMetadata(ctx, id, id, "query", 10); return err }},
		{"get", func() error { _, err := s.GetArticle(ctx, id, id, id); return err }},
		{"get links", func() error { _, err := s.GetArticleLinks(ctx, id, id, id); return err }},
		{"list", func() error { _, err := s.ListArticles(ctx, id); return err }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.call(); err == nil {
				t.Fatal("operation succeeded with a closed database")
			}
		})
	}
}
