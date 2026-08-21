package store

import (
	"context"
	"testing"
)

func TestLastProject(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if value, ok, err := st.LastProject(ctx); err != nil || ok || value != "" {
		t.Fatalf("LastProject(unset) = %q, %v, %v", value, ok, err)
	}
	for _, want := range []string{"one", "two"} {
		if err := st.SetLastProject(ctx, want); err != nil {
			t.Fatalf("SetLastProject(%q) error = %v", want, err)
		}
		if value, ok, err := st.LastProject(ctx); err != nil || !ok || value != want {
			t.Fatalf("LastProject() = %q, %v, %v", value, ok, err)
		}
	}
	if err := st.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if err := st.SetLastProject(ctx, "closed"); err == nil {
		t.Fatal("SetLastProject(closed) error = nil")
	}
	if _, _, err := st.LastProject(ctx); err == nil {
		t.Fatal("LastProject(closed) error = nil")
	}
}
