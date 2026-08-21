package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Project is a stored project and its Kubernetes defaults.
type Project struct {
	ID                     int64
	Name                   string
	RootPath               string
	KubeContext            string
	KubeServer             string
	Namespace              string
	SwitchPromptSuppressed bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// ProjectMeta contains the editable fields of a project.
type ProjectMeta struct {
	Name, RootPath, KubeContext, KubeServer, Namespace string
	SwitchPromptSuppressed                             bool
}

const projectColumns = "id, name, root_path, kube_context, kube_server, namespace, switch_prompt_suppressed, created_at, updated_at"

// CreateProject stores a new project.
func (s *Store) CreateProject(ctx context.Context, meta ProjectMeta) (Project, error) {
	return s.CreateProjectWithNamespaces(ctx, meta, nil)
}

// CreateProjectWithNamespaces atomically stores a new project and its additional namespaces.
func (s *Store) CreateProjectWithNamespaces(ctx context.Context, meta ProjectMeta, namespaces []string) (Project, error) {
	if err := validateProjectMeta(meta); err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("begin creating project %q: %w", meta.Name, err)
	}
	defer func() { _ = tx.Rollback() }()

	now := timeNow().UTC().Format(time.RFC3339)
	result, err := tx.ExecContext(ctx, `INSERT INTO projects(name, root_path, kube_context, kube_server, namespace, switch_prompt_suppressed, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, meta.Name, meta.RootPath, meta.KubeContext, meta.KubeServer, meta.Namespace, meta.SwitchPromptSuppressed, now, now)
	if err != nil {
		if isConstraint(err) {
			return Project{}, fmt.Errorf("create project %q: %w", meta.Name, ErrDuplicate)
		}
		return Project{}, fmt.Errorf("create project %q: %w", meta.Name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Project{}, fmt.Errorf("read project %q id: %w", meta.Name, err)
	}
	if err := insertProjectNamespaces(ctx, tx, id, namespaces); err != nil {
		return Project{}, fmt.Errorf("create project %q: %w", meta.Name, err)
	}
	project, err := scanProject(tx.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
	if err != nil {
		return Project{}, fmt.Errorf("read created project %q: %w", meta.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("commit creating project %q: %w", meta.Name, err)
	}
	return project, nil
}

// UpdateProjectWithNamespaces atomically replaces a project's editable fields and additional namespaces.
func (s *Store) UpdateProjectWithNamespaces(ctx context.Context, id int64, meta ProjectMeta, namespaces []string) (Project, error) {
	if err := validateProjectMeta(meta); err != nil {
		return Project{}, fmt.Errorf("update project %d: %w", id, err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("begin updating project %d: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE projects SET name=?, root_path=?, kube_context=?, kube_server=?, namespace=?, switch_prompt_suppressed=?, updated_at=? WHERE id=?`, meta.Name, meta.RootPath, meta.KubeContext, meta.KubeServer, meta.Namespace, meta.SwitchPromptSuppressed, timeNow().UTC().Format(time.RFC3339), id)
	if err != nil {
		if isConstraint(err) {
			return Project{}, fmt.Errorf("update project %d: %w", id, ErrDuplicate)
		}
		return Project{}, fmt.Errorf("update project %d: %w", id, err)
	}
	if err := requireAffected(result, fmt.Sprintf("project %d", id)); err != nil {
		return Project{}, err
	}
	if err := replaceProjectNamespaces(ctx, tx, id, namespaces); err != nil {
		return Project{}, err
	}
	project, err := scanProject(tx.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
	if err != nil {
		return Project{}, fmt.Errorf("read updated project %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("commit updating project %d: %w", id, err)
	}
	return project, nil
}

// BackfillProjectKubeServer records a legacy project's server without replacing an existing identity.
func (s *Store) BackfillProjectKubeServer(ctx context.Context, projectID int64, kubeServer string) error {
	if strings.TrimSpace(kubeServer) == "" {
		return fmt.Errorf("backfill project %d kube server: server must not be empty", projectID)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET kube_server=CASE WHEN kube_server='' THEN ? ELSE kube_server END, updated_at=CASE WHEN kube_server='' THEN ? ELSE updated_at END WHERE id=?`, kubeServer, timeNow().UTC().Format(time.RFC3339), projectID)
	if err != nil {
		return fmt.Errorf("backfill project %d kube server: %w", projectID, err)
	}
	return requireAffected(result, fmt.Sprintf("project %d", projectID))
}

// ConfirmProjectContext records a context binding and optionally trusts future context switches.
func (s *Store) ConfirmProjectContext(ctx context.Context, projectID int64, kubeContext, kubeServer string, suppressSwitchPrompt bool) (Project, error) {
	if strings.TrimSpace(kubeServer) == "" {
		return Project{}, fmt.Errorf("confirm project %d context: server must not be empty", projectID)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE projects SET kube_context=?, kube_server=?, switch_prompt_suppressed=CASE WHEN ? THEN 1 ELSE switch_prompt_suppressed END, updated_at=? WHERE id=?`, kubeContext, kubeServer, suppressSwitchPrompt, timeNow().UTC().Format(time.RFC3339), projectID)
	if err != nil {
		return Project{}, fmt.Errorf("confirm project %d context: %w", projectID, err)
	}
	if err := requireAffected(result, fmt.Sprintf("project %d", projectID)); err != nil {
		return Project{}, err
	}
	return s.projectByID(ctx, projectID)
}

// DeleteProject deletes a project and its linked records.
func (s *Store) DeleteProject(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete project %d: %w", id, err)
	}
	return requireAffected(result, fmt.Sprintf("project %d", id))
}

// ListProjects returns every project ordered by name.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer func() { _ = rows.Close() }()
	projects := make([]Project, 0)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

// ProjectByName returns the project with name or ErrNotFound.
func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	project, err := scanProject(s.db.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE name=?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("project %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return Project{}, fmt.Errorf("read project %q: %w", name, err)
	}
	return project, nil
}

// ProjectByRoot returns the project with rootPath or ErrNotFound.
func (s *Store) ProjectByRoot(ctx context.Context, rootPath string) (Project, error) {
	project, err := scanProject(s.db.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE root_path=?", rootPath))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("project root %q: %w", rootPath, ErrNotFound)
	}
	if err != nil {
		return Project{}, fmt.Errorf("read project root %q: %w", rootPath, err)
	}
	return project, nil
}

func (s *Store) projectByID(ctx context.Context, id int64) (Project, error) {
	project, err := scanProject(s.db.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id=?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("project %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return Project{}, fmt.Errorf("read project %d: %w", id, err)
	}
	return project, nil
}

type projectScanner interface{ Scan(...any) error }

func scanProject(scanner projectScanner) (Project, error) {
	var project Project
	var createdAt, updatedAt string
	if err := scanner.Scan(&project.ID, &project.Name, &project.RootPath, &project.KubeContext, &project.KubeServer, &project.Namespace, &project.SwitchPromptSuppressed, &createdAt, &updatedAt); err != nil {
		return Project{}, err
	}
	var err error
	project.CreatedAt, err = time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return Project{}, fmt.Errorf("parse created_at: %w", err)
	}
	project.UpdatedAt, err = time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return project, nil
}

func validateProjectMeta(meta ProjectMeta) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "name", value: meta.Name},
		{name: "root path", value: meta.RootPath},
		{name: "kube context", value: meta.KubeContext},
		{name: "namespace", value: meta.Namespace},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s must not be empty", field.name)
		}
	}
	return nil
}

func requireAffected(result sql.Result, subject string) error {
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read affected rows for %s: %w", subject, err)
	}
	if count == 0 {
		return fmt.Errorf("%s: %w", subject, ErrNotFound)
	}
	return nil
}

func insertProjectNamespaces(ctx context.Context, tx *sql.Tx, projectID int64, namespaces []string) error {
	for _, namespace := range namespaces {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO project_namespaces(project_id, namespace) VALUES (?, ?)`, projectID, namespace); err != nil {
			return fmt.Errorf("add namespace %q to project %d: %w", namespace, projectID, err)
		}
	}
	return nil
}

func replaceProjectNamespaces(ctx context.Context, tx *sql.Tx, projectID int64, namespaces []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_namespaces WHERE project_id=?`, projectID); err != nil {
		return fmt.Errorf("clear namespaces for project %d: %w", projectID, err)
	}
	return insertProjectNamespaces(ctx, tx, projectID, namespaces)
}

// SetNamespaces replaces the additional namespaces associated with a project.
func (s *Store) SetNamespaces(ctx context.Context, projectID int64, namespaces []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replacing namespaces for project %d: %w", projectID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceProjectNamespaces(ctx, tx, projectID, namespaces); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace namespaces for project %d: %w", projectID, err)
	}
	return nil
}

// Namespaces returns a project's additional namespaces ordered by name.
func (s *Store) Namespaces(ctx context.Context, projectID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT namespace FROM project_namespaces WHERE project_id=? ORDER BY namespace`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list namespaces for project %d: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	namespaces := make([]string, 0)
	for rows.Next() {
		var namespace string
		if err := rows.Scan(&namespace); err != nil {
			return nil, fmt.Errorf("scan namespace for project %d: %w", projectID, err)
		}
		namespaces = append(namespaces, namespace)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate namespaces for project %d: %w", projectID, err)
	}
	return namespaces, nil
}

// WorkloadLink identifies a Kubernetes workload linked to a project.
type WorkloadLink struct{ Kind, Namespace, Name, OriginContext, OriginServer string }

// LinkWorkload links a workload to a project if it is not already linked.
func (s *Store) LinkWorkload(ctx context.Context, projectID int64, link WorkloadLink) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO project_workloads(project_id, kind, namespace, name, origin_context, origin_server) VALUES (?, ?, ?, ?, ?, ?)`, projectID, link.Kind, link.Namespace, link.Name, link.OriginContext, link.OriginServer)
	if err != nil {
		return fmt.Errorf("link workload %s/%s to project %d: %w", link.Kind, link.Name, projectID, err)
	}
	return nil
}

// UnlinkWorkload removes a workload link from a project.
func (s *Store) UnlinkWorkload(ctx context.Context, projectID int64, link WorkloadLink) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_workloads WHERE project_id=? AND kind=? AND namespace=? AND name=?`, projectID, link.Kind, link.Namespace, link.Name)
	if err != nil {
		return fmt.Errorf("unlink workload %s/%s from project %d: %w", link.Kind, link.Name, projectID, err)
	}
	return nil
}

// WorkloadLinks returns a project's workload links in stable order.
func (s *Store) WorkloadLinks(ctx context.Context, projectID int64) ([]WorkloadLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, namespace, name, origin_context, origin_server FROM project_workloads WHERE project_id=? ORDER BY kind, namespace, name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list workload links for project %d: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	links := make([]WorkloadLink, 0)
	for rows.Next() {
		var link WorkloadLink
		if err := rows.Scan(&link.Kind, &link.Namespace, &link.Name, &link.OriginContext, &link.OriginServer); err != nil {
			return nil, fmt.Errorf("scan workload link: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workload links: %w", err)
	}
	return links, nil
}

const (
	// SourceManual identifies a resource linked directly by the user.
	SourceManual = "manual"
	// SourceWorkload identifies a resource linked through a workload.
	SourceWorkload = "workload"
	// SourceScan identifies a resource linked by the repository scanner.
	SourceScan = "scan"
)

// ResourceLink identifies a Kubernetes resource linked to a project and its origin.
type ResourceLink struct{ Kind, Namespace, Name, Source, OriginContext, OriginServer string }

// LinkResource links a resource to a project, updating the source of an existing link.
func (s *Store) LinkResource(ctx context.Context, projectID int64, link ResourceLink) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_resources(project_id, kind, namespace, name, source, origin_context, origin_server) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(project_id, kind, namespace, name) DO UPDATE SET source=excluded.source, origin_context=excluded.origin_context, origin_server=excluded.origin_server`, projectID, link.Kind, link.Namespace, link.Name, link.Source, link.OriginContext, link.OriginServer)
	if err != nil {
		return fmt.Errorf("link resource %s/%s to project %d: %w", link.Kind, link.Name, projectID, err)
	}
	return nil
}

// UnlinkResource removes a resource link from a project.
func (s *Store) UnlinkResource(ctx context.Context, projectID int64, kind, namespace, name string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM project_resources WHERE project_id=? AND kind=? AND namespace=? AND name=?`, projectID, kind, namespace, name)
	if err != nil {
		return fmt.Errorf("unlink resource %s/%s from project %d: %w", kind, name, projectID, err)
	}
	return nil
}

// ResourceLinks returns a project's resource links in stable order.
func (s *Store) ResourceLinks(ctx context.Context, projectID int64) ([]ResourceLink, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, namespace, name, source, origin_context, origin_server FROM project_resources WHERE project_id=? ORDER BY kind, namespace, name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list resource links for project %d: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	links := make([]ResourceLink, 0)
	for rows.Next() {
		var link ResourceLink
		if err := rows.Scan(&link.Kind, &link.Namespace, &link.Name, &link.Source, &link.OriginContext, &link.OriginServer); err != nil {
			return nil, fmt.Errorf("scan resource link: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource links: %w", err)
	}
	return links, nil
}
