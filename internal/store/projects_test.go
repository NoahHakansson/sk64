package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProjectCRUD(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	oldNow := timeNow
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	timeNow = func() time.Time { return createdAt }
	t.Cleanup(func() { timeNow = oldNow })
	createdMeta := validMeta("zeta", "/zeta")
	createdMeta.KubeServer = "https://cluster.example"
	createdMeta.SwitchPromptSuppressed = true
	created, err := st.CreateProject(ctx, createdMeta)
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	alpha, err := st.CreateProject(ctx, validMeta("alpha", "/alpha"))
	if err != nil {
		t.Fatalf("CreateProject(alpha) error = %v", err)
	}
	if created.KubeServer != createdMeta.KubeServer || !created.SwitchPromptSuppressed {
		t.Fatalf("created identity = server %q suppressed %t", created.KubeServer, created.SwitchPromptSuppressed)
	}
	if !created.CreatedAt.Equal(createdAt) || !created.UpdatedAt.Equal(createdAt) {
		t.Fatalf("created timestamps = %v %v", created.CreatedAt, created.UpdatedAt)
	}
	byName, err := st.ProjectByName(ctx, "zeta")
	if err != nil || byName.ID != created.ID || byName.KubeServer != createdMeta.KubeServer || !byName.SwitchPromptSuppressed {
		t.Fatalf("ProjectByName() = %+v, %v", byName, err)
	}
	byRoot, err := st.ProjectByRoot(ctx, "/zeta")
	if err != nil || byRoot.ID != created.ID {
		t.Fatalf("ProjectByRoot() = %+v, %v", byRoot, err)
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projects) != 2 || projects[0].ID != alpha.ID || projects[1].ID != created.ID || projects[1].KubeServer != createdMeta.KubeServer || !projects[1].SwitchPromptSuppressed {
		t.Fatalf("ListProjects() = %+v", projects)
	}
	updatedAt := createdAt.Add(time.Hour)
	timeNow = func() time.Time { return updatedAt }
	updated, err := st.UpdateProjectWithNamespaces(ctx, created.ID, ProjectMeta{Name: "omega", RootPath: "/omega", KubeContext: "other", KubeServer: "https://other.example", Namespace: "prod"}, nil)
	if err != nil {
		t.Fatalf("UpdateProjectWithNamespaces() error = %v", err)
	}
	if updated.Name != "omega" || updated.RootPath != "/omega" || updated.KubeContext != "other" || updated.KubeServer != "https://other.example" || updated.Namespace != "prod" || updated.SwitchPromptSuppressed || !updated.CreatedAt.Equal(createdAt) || !updated.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("UpdateProjectWithNamespaces() = %+v", updated)
	}
	if err := st.DeleteProject(ctx, created.ID); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	if _, err := st.UpdateProjectWithNamespaces(ctx, created.ID, validMeta("absent", "/absent"), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateProjectWithNamespaces(absent) error = %v, want ErrNotFound", err)
	}
	if err := st.DeleteProject(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteProject(absent) error = %v, want ErrNotFound", err)
	}
	for _, lookup := range []func() error{
		func() error { _, err := st.ProjectByName(ctx, "omega"); return err },
		func() error { _, err := st.ProjectByRoot(ctx, "/omega"); return err },
	} {
		if err := lookup(); !errors.Is(err, ErrNotFound) {
			t.Fatalf("absent lookup error = %v, want ErrNotFound", err)
		}
	}
}

func TestCreateProjectWithNamespaces(t *testing.T) {
	tests := []struct {
		name              string
		namespaces        []string
		rejectNamespace   string
		wantNamespaces    []string
		wantErrorContains string
	}{
		{name: "no additional namespaces"},
		{name: "stores unique namespaces in sorted order", namespaces: []string{"z", "a", "z"}, wantNamespaces: []string{"a", "z"}},
		{name: "namespace failure rolls back project", namespaces: []string{"saved", "reject"}, rejectNamespace: "reject", wantErrorContains: `add namespace "reject"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openTestStore(t)
			if test.rejectNamespace != "" {
				_, err := st.db.Exec(`CREATE TRIGGER reject_project_namespace BEFORE INSERT ON project_namespaces WHEN NEW.namespace = 'reject' BEGIN SELECT RAISE(ABORT, 'namespace rejected'); END`)
				if err != nil {
					t.Fatalf("create rejection trigger: %v", err)
				}
			}

			meta := validMeta("atomic", "/atomic")
			project, err := st.CreateProjectWithNamespaces(t.Context(), meta, test.namespaces)
			if test.wantErrorContains != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrorContains) {
					t.Fatalf("CreateProjectWithNamespaces() error = %v, want %q", err, test.wantErrorContains)
				}
				if _, lookupErr := st.ProjectByName(t.Context(), meta.Name); !errors.Is(lookupErr, ErrNotFound) {
					t.Fatalf("ProjectByName() after rollback error = %v, want ErrNotFound", lookupErr)
				}
				var namespaceCount int
				if countErr := st.db.QueryRow(`SELECT COUNT(*) FROM project_namespaces`).Scan(&namespaceCount); countErr != nil {
					t.Fatalf("count namespaces after rollback: %v", countErr)
				}
				if namespaceCount != 0 {
					t.Fatalf("namespace count after rollback = %d, want 0", namespaceCount)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreateProjectWithNamespaces() error = %v", err)
			}
			gotNamespaces, err := st.Namespaces(t.Context(), project.ID)
			if err != nil {
				t.Fatalf("Namespaces() error = %v", err)
			}
			if strings.Join(gotNamespaces, ",") != strings.Join(test.wantNamespaces, ",") {
				t.Fatalf("Namespaces() = %v, want %v", gotNamespaces, test.wantNamespaces)
			}
		})
	}
}

func TestUpdateProjectWithNamespaces(t *testing.T) {
	t.Run("updates metadata and namespaces", func(t *testing.T) {
		st := openTestStore(t)
		project, err := st.CreateProjectWithNamespaces(t.Context(), validMeta("original", "/original"), []string{"old"})
		if err != nil {
			t.Fatalf("CreateProjectWithNamespaces() error = %v", err)
		}

		meta := validMeta("updated", "/updated")
		meta.KubeServer = "https://updated.example"
		updated, err := st.UpdateProjectWithNamespaces(t.Context(), project.ID, meta, []string{"z", "a", "z"})
		if err != nil {
			t.Fatalf("UpdateProjectWithNamespaces() error = %v", err)
		}
		if updated.Name != meta.Name || updated.RootPath != meta.RootPath || updated.KubeServer != meta.KubeServer {
			t.Fatalf("UpdateProjectWithNamespaces() = %+v", updated)
		}
		namespaces, err := st.Namespaces(t.Context(), project.ID)
		if err != nil {
			t.Fatalf("Namespaces() error = %v", err)
		}
		if !reflect.DeepEqual(namespaces, []string{"a", "z"}) {
			t.Fatalf("Namespaces() = %v, want [a z]", namespaces)
		}
	})

	t.Run("namespace failure rolls back metadata and namespaces", func(t *testing.T) {
		st := openTestStore(t)
		originalMeta := validMeta("original", "/original")
		project, err := st.CreateProjectWithNamespaces(t.Context(), originalMeta, []string{"old-a", "old-b"})
		if err != nil {
			t.Fatalf("CreateProjectWithNamespaces() error = %v", err)
		}
		if _, err := st.db.Exec(`CREATE TRIGGER reject_project_namespace BEFORE INSERT ON project_namespaces WHEN NEW.namespace = 'reject' BEGIN SELECT RAISE(ABORT, 'namespace rejected'); END`); err != nil {
			t.Fatalf("create rejection trigger: %v", err)
		}

		_, err = st.UpdateProjectWithNamespaces(t.Context(), project.ID, validMeta("changed", "/changed"), []string{"saved", "reject"})
		if err == nil || !strings.Contains(err.Error(), `add namespace "reject"`) {
			t.Fatalf("UpdateProjectWithNamespaces() error = %v, want namespace failure", err)
		}
		stored, err := st.ProjectByName(t.Context(), originalMeta.Name)
		if err != nil {
			t.Fatalf("ProjectByName(original) error = %v", err)
		}
		if stored.RootPath != originalMeta.RootPath {
			t.Fatalf("stored project after rollback = %+v", stored)
		}
		if _, err := st.ProjectByName(t.Context(), "changed"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("ProjectByName(changed) error = %v, want ErrNotFound", err)
		}
		namespaces, err := st.Namespaces(t.Context(), project.ID)
		if err != nil {
			t.Fatalf("Namespaces() error = %v", err)
		}
		if !reflect.DeepEqual(namespaces, []string{"old-a", "old-b"}) {
			t.Fatalf("Namespaces() after rollback = %v, want [old-a old-b]", namespaces)
		}
	})
}

func TestBackfillProjectKubeServer(t *testing.T) {
	tests := []struct {
		name          string
		storedServer  string
		projectExists bool
		backfill      string
		wantServer    string
		wantNotFound  bool
		wantError     string
	}{
		{name: "legacy project", projectExists: true, backfill: "https://cluster.example", wantServer: "https://cluster.example"},
		{name: "established identity is preserved", storedServer: "https://original.example", projectExists: true, backfill: "https://replacement.example", wantServer: "https://original.example"},
		{name: "missing project", backfill: "https://cluster.example", wantNotFound: true},
		{name: "empty server", projectExists: true, backfill: "  ", wantError: "server must not be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openTestStore(t)
			projectID := int64(9999)
			if test.projectExists {
				meta := validMeta("project", "/project")
				meta.KubeServer = test.storedServer
				project, err := st.CreateProject(t.Context(), meta)
				if err != nil {
					t.Fatalf("CreateProject() error = %v", err)
				}
				projectID = project.ID
			}

			err := st.BackfillProjectKubeServer(t.Context(), projectID, test.backfill)
			if test.wantNotFound {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("BackfillProjectKubeServer() error = %v, want ErrNotFound", err)
				}
				return
			}
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("BackfillProjectKubeServer() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("BackfillProjectKubeServer() error = %v", err)
			}
			project, err := st.projectByID(t.Context(), projectID)
			if err != nil {
				t.Fatalf("projectByID() error = %v", err)
			}
			if project.KubeServer != test.wantServer {
				t.Fatalf("KubeServer = %q, want %q", project.KubeServer, test.wantServer)
			}
		})
	}
}

func TestConfirmProjectContext(t *testing.T) {
	tests := []struct {
		name      string
		context   string
		server    string
		suppress  bool
		missing   bool
		wantError string
	}{
		{name: "bind once", context: "ctx", server: "https://cluster.example"},
		{name: "bind and suppress", context: "ctx", server: "https://cluster.example", suppress: true},
		{name: "rename context", context: "renamed", server: "https://cluster.example"},
		{name: "missing project", context: "ctx", server: "https://cluster.example", missing: true},
		{name: "empty server", context: "ctx", server: " ", wantError: "server must not be empty"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st := openTestStore(t)
			project, err := st.CreateProject(t.Context(), validMeta("project", "/project"))
			if err != nil {
				t.Fatalf("CreateProject() error = %v", err)
			}
			projectID := project.ID
			if test.missing {
				projectID++
			}
			confirmed, err := st.ConfirmProjectContext(t.Context(), projectID, test.context, test.server, test.suppress)
			if test.missing {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("ConfirmProjectContext() error = %v, want ErrNotFound", err)
				}
				return
			}
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("ConfirmProjectContext() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConfirmProjectContext() error = %v", err)
			}
			if confirmed.KubeContext != test.context || confirmed.KubeServer != test.server || confirmed.SwitchPromptSuppressed != test.suppress {
				t.Fatalf("confirmed identity = context %q server %q suppressed %t", confirmed.KubeContext, confirmed.KubeServer, confirmed.SwitchPromptSuppressed)
			}
			roundTrip, err := st.ProjectByName(t.Context(), project.Name)
			if err != nil {
				t.Fatalf("ProjectByName() error = %v", err)
			}
			if roundTrip.KubeContext != test.context || roundTrip.KubeServer != test.server {
				t.Fatalf("round-trip identity = context %q server %q", roundTrip.KubeContext, roundTrip.KubeServer)
			}
		})
	}
}

func TestProjectStoredTimestampErrors(t *testing.T) {
	tests := []struct {
		name       string
		column     string
		corruptSQL string
	}{
		{name: "created timestamp", column: "created_at", corruptSQL: `UPDATE projects SET created_at='invalid' WHERE id=?`},
		{name: "updated timestamp", column: "updated_at", corruptSQL: `UPDATE projects SET updated_at='invalid' WHERE id=?`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			st, project := storeWithProject(t)
			if _, err := st.db.Exec(test.corruptSQL, project.ID); err != nil {
				t.Fatalf("corrupt %s: %v", test.column, err)
			}
			if _, err := st.ListProjects(t.Context()); err == nil || !strings.Contains(err.Error(), "parse "+test.column) {
				t.Fatalf("ListProjects() error = %v, want parse %s", err, test.column)
			}
		})
	}
}

func TestProjectByIDErrors(t *testing.T) {
	st := openTestStore(t)
	if _, err := st.projectByID(t.Context(), 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("projectByID(missing) error = %v, want ErrNotFound", err)
	}
	if err := st.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
	if _, err := st.projectByID(t.Context(), 1); err == nil {
		t.Fatal("projectByID(closed) error = nil")
	}
}

func TestProjectDuplicates(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, validMeta("one", "/one")); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	for _, meta := range []ProjectMeta{validMeta("one", "/two"), validMeta("two", "/one")} {
		if _, err := st.CreateProject(ctx, meta); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("CreateProject(%+v) error = %v, want ErrDuplicate", meta, err)
		}
	}
	second, err := st.CreateProject(ctx, validMeta("second", "/second"))
	if err != nil {
		t.Fatalf("seed second project: %v", err)
	}
	for _, meta := range []ProjectMeta{validMeta("one", "/changed"), validMeta("changed", "/one")} {
		if _, err := st.UpdateProjectWithNamespaces(ctx, second.ID, meta, nil); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("UpdateProjectWithNamespaces(%+v) error = %v, want ErrDuplicate", meta, err)
		}
	}
	for _, meta := range []ProjectMeta{
		{Name: "", RootPath: "/x", KubeContext: "ctx", Namespace: "ns"},
		{Name: "x", RootPath: "", KubeContext: "ctx", Namespace: "ns"},
		{Name: "x", RootPath: "/x", KubeContext: "", Namespace: "ns"},
		{Name: "x", RootPath: "/x", KubeContext: "ctx", Namespace: ""},
	} {
		if _, err := st.CreateProject(ctx, meta); err == nil {
			t.Fatalf("CreateProject(%+v) error = nil", meta)
		}
	}
	if _, err := st.UpdateProjectWithNamespaces(ctx, second.ID, ProjectMeta{}, nil); err == nil {
		t.Fatal("UpdateProjectWithNamespaces(empty) error = nil")
	} else if !strings.Contains(err.Error(), "name must not be empty") {
		t.Fatalf("UpdateProjectWithNamespaces(empty) error = %q, want deterministic name error", err)
	}
	if err := st.db.Close(); err != nil {
		t.Fatalf("close database for failure checks: %v", err)
	}
	if _, err := st.CreateProject(ctx, validMeta("closed", "/closed")); err == nil {
		t.Fatal("CreateProject(closed) error = nil")
	}
	if _, err := st.UpdateProjectWithNamespaces(ctx, second.ID, validMeta("closed", "/closed"), nil); err == nil {
		t.Fatal("UpdateProjectWithNamespaces(closed) error = nil")
	}
	if err := st.DeleteProject(ctx, second.ID); err == nil {
		t.Fatal("DeleteProject(closed) error = nil")
	}
	if _, err := st.ListProjects(ctx); err == nil {
		t.Fatal("ListProjects(closed) error = nil")
	}
	if _, err := st.ProjectByName(ctx, "one"); err == nil {
		t.Fatal("ProjectByName(closed) error = nil")
	}
	if _, err := st.ProjectByRoot(ctx, "/one"); err == nil {
		t.Fatal("ProjectByRoot(closed) error = nil")
	}
}

func TestNamespaces(t *testing.T) {
	st, project := storeWithProject(t)
	ctx := context.Background()
	if err := st.SetNamespaces(ctx, project.ID, []string{"z", "a"}); err != nil {
		t.Fatalf("SetNamespaces(A) error = %v", err)
	}
	if err := st.SetNamespaces(ctx, project.ID, []string{"c", "b", "c"}); err != nil {
		t.Fatalf("SetNamespaces(B) error = %v", err)
	}
	got, err := st.Namespaces(ctx, project.ID)
	if err != nil || len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Fatalf("Namespaces() = %v, %v", got, err)
	}
	if err := st.SetNamespaces(ctx, project.ID, nil); err != nil {
		t.Fatalf("SetNamespaces(clear) error = %v", err)
	}
	got, err = st.Namespaces(ctx, project.ID)
	if err != nil || len(got) != 0 {
		t.Fatalf("Namespaces() after clear = %v, %v", got, err)
	}
	if err := st.SetNamespaces(ctx, 9999, []string{"missing"}); err == nil {
		t.Fatal("SetNamespaces(missing project) error = nil")
	}
	if err := st.db.Close(); err != nil {
		t.Fatalf("close database for failure checks: %v", err)
	}
	if err := st.SetNamespaces(ctx, project.ID, nil); err == nil {
		t.Fatal("SetNamespaces(closed) error = nil")
	}
	if _, err := st.Namespaces(ctx, project.ID); err == nil {
		t.Fatal("Namespaces(closed) error = nil")
	}
}

func TestWorkloadLinks(t *testing.T) {
	st, project := storeWithProject(t)
	ctx := context.Background()
	links := []WorkloadLink{
		{Kind: "StatefulSet", Namespace: "b", Name: "z", OriginContext: "ctx-b", OriginServer: "https://b.example"},
		{Kind: "Deployment", Namespace: "a", Name: "web", OriginContext: "ctx-a", OriginServer: "https://a.example"},
	}
	for _, link := range links {
		if err := st.LinkWorkload(ctx, project.ID, link); err != nil {
			t.Fatalf("LinkWorkload() error = %v", err)
		}
	}
	if err := st.LinkWorkload(ctx, project.ID, links[0]); err != nil {
		t.Fatalf("LinkWorkload(double) error = %v", err)
	}
	got, err := st.WorkloadLinks(ctx, project.ID)
	if err != nil || len(got) != 2 || got[0] != links[1] || got[1] != links[0] {
		t.Fatalf("WorkloadLinks() = %+v, %v", got, err)
	}
	if err := st.UnlinkWorkload(ctx, project.ID, links[0]); err != nil {
		t.Fatalf("UnlinkWorkload() error = %v", err)
	}
	if err := st.UnlinkWorkload(ctx, project.ID, links[0]); err != nil {
		t.Fatalf("UnlinkWorkload(absent) error = %v", err)
	}
	if err := st.db.Close(); err != nil {
		t.Fatalf("close database for failure checks: %v", err)
	}
	if err := st.LinkWorkload(ctx, project.ID, links[0]); err == nil {
		t.Fatal("LinkWorkload(closed) error = nil")
	}
	if err := st.UnlinkWorkload(ctx, project.ID, links[0]); err == nil {
		t.Fatal("UnlinkWorkload(closed) error = nil")
	}
	if _, err := st.WorkloadLinks(ctx, project.ID); err == nil {
		t.Fatal("WorkloadLinks(closed) error = nil")
	}
}

func TestResourceLinks(t *testing.T) {
	st, project := storeWithProject(t)
	ctx := context.Background()
	secret := ResourceLink{Kind: "Secret", Namespace: "b", Name: "z", Source: SourceManual, OriginContext: "ctx-b", OriginServer: "https://b.example"}
	configMap := ResourceLink{Kind: "ConfigMap", Namespace: "a", Name: "settings", Source: SourceManual, OriginContext: "ctx-a", OriginServer: "https://a.example"}
	if err := st.LinkResource(ctx, project.ID, secret); err != nil {
		t.Fatalf("LinkResource(secret) error = %v", err)
	}
	secret.Source = SourceScan
	secret.OriginContext = "ctx-new"
	secret.OriginServer = "https://new.example"
	if err := st.LinkResource(ctx, project.ID, secret); err != nil {
		t.Fatalf("LinkResource(upsert) error = %v", err)
	}
	if err := st.LinkResource(ctx, project.ID, configMap); err != nil {
		t.Fatalf("LinkResource(configmap) error = %v", err)
	}
	got, err := st.ResourceLinks(ctx, project.ID)
	if err != nil || len(got) != 2 || got[0] != configMap || got[1] != secret {
		t.Fatalf("ResourceLinks() = %+v, %v", got, err)
	}
	if err := st.UnlinkResource(ctx, project.ID, secret.Kind, secret.Namespace, secret.Name); err != nil {
		t.Fatalf("UnlinkResource() error = %v", err)
	}
	for _, invalid := range []ResourceLink{
		{Kind: "Service", Namespace: "a", Name: "x", Source: SourceManual},
		{Kind: "Secret", Namespace: "a", Name: "x", Source: "other"},
	} {
		err := st.LinkResource(ctx, project.ID, invalid)
		if err == nil || errors.Is(err, ErrDuplicate) {
			t.Fatalf("LinkResource(%+v) error = %v", invalid, err)
		}
	}
	if err := st.db.Close(); err != nil {
		t.Fatalf("close database for failure checks: %v", err)
	}
	if err := st.LinkResource(ctx, project.ID, configMap); err == nil {
		t.Fatal("LinkResource(closed) error = nil")
	}
	if err := st.UnlinkResource(ctx, project.ID, configMap.Kind, configMap.Namespace, configMap.Name); err == nil {
		t.Fatal("UnlinkResource(closed) error = nil")
	}
	if _, err := st.ResourceLinks(ctx, project.ID); err == nil {
		t.Fatal("ResourceLinks(closed) error = nil")
	}
}

func TestCascadeDelete(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	first, _ := st.CreateProject(ctx, validMeta("first", "/first"))
	second, _ := st.CreateProject(ctx, validMeta("second", "/second"))
	for _, project := range []Project{first, second} {
		_ = st.SetNamespaces(ctx, project.ID, []string{"a", "b"})
		_ = st.LinkWorkload(ctx, project.ID, WorkloadLink{Kind: "Deployment", Namespace: "a", Name: "one"})
		_ = st.LinkWorkload(ctx, project.ID, WorkloadLink{Kind: "Job", Namespace: "b", Name: "two"})
		_ = st.LinkResource(ctx, project.ID, ResourceLink{Kind: "Secret", Namespace: "a", Name: "one", Source: SourceManual})
		_ = st.LinkResource(ctx, project.ID, ResourceLink{Kind: "ConfigMap", Namespace: "b", Name: "two", Source: SourceManual})
	}
	if err := st.DeleteProject(ctx, first.ID); err != nil {
		t.Fatalf("DeleteProject() error = %v", err)
	}
	for _, table := range []string{"project_namespaces", "project_workloads", "project_resources"} {
		var firstCount, secondCount int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, first.ID).Scan(&firstCount); err != nil {
			t.Fatalf("count first %s: %v", table, err)
		}
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, second.ID).Scan(&secondCount); err != nil {
			t.Fatalf("count second %s: %v", table, err)
		}
		if firstCount != 0 || secondCount != 2 {
			t.Fatalf("%s counts = %d, %d", table, firstCount, secondCount)
		}
	}
}

func TestMutationsRejectUnknownProject(t *testing.T) {
	st := openTestStore(t)
	ctx := t.Context()
	const missingID = int64(9999)
	tests := []struct {
		name         string
		mutate       func() error
		wantNotFound bool
	}{
		{name: "set namespaces", mutate: func() error { return st.SetNamespaces(ctx, missingID, []string{"default"}) }},
		{name: "link workload", mutate: func() error {
			return st.LinkWorkload(ctx, missingID, WorkloadLink{Kind: "Deployment", Namespace: "default", Name: "web"})
		}},
		{name: "link resource", mutate: func() error {
			return st.LinkResource(ctx, missingID, ResourceLink{Kind: "Secret", Namespace: "default", Name: "creds", Source: SourceManual})
		}},
		{name: "update project", mutate: func() error {
			_, err := st.UpdateProjectWithNamespaces(ctx, missingID, validMeta("missing", "/missing"), nil)
			return err
		}, wantNotFound: true},
		{name: "delete project", mutate: func() error { return st.DeleteProject(ctx, missingID) }, wantNotFound: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.mutate()
			if err == nil {
				t.Fatal("mutation error = nil")
			}
			if test.wantNotFound && !errors.Is(err, ErrNotFound) {
				t.Fatalf("mutation error = %v, want ErrNotFound", err)
			}
		})
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "sk64.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func storeWithProject(t *testing.T) (*Store, Project) {
	t.Helper()
	st := openTestStore(t)
	project, err := st.CreateProject(context.Background(), validMeta("project", "/project"))
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	return st, project
}
