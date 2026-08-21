CREATE TABLE projects (
  id           INTEGER PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  root_path    TEXT NOT NULL UNIQUE,
  kube_context TEXT NOT NULL,
  namespace    TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);
CREATE TABLE project_namespaces (
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  namespace  TEXT NOT NULL,
  PRIMARY KEY (project_id, namespace)
);
CREATE TABLE project_workloads (
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  namespace  TEXT NOT NULL,
  name       TEXT NOT NULL,
  PRIMARY KEY (project_id, kind, namespace, name)
);
CREATE TABLE project_resources (
  project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('Secret','ConfigMap')),
  namespace  TEXT NOT NULL,
  name       TEXT NOT NULL,
  source     TEXT NOT NULL CHECK (source IN ('manual','workload','scan')),
  PRIMARY KEY (project_id, kind, namespace, name)
);
CREATE TABLE ui_state (key TEXT PRIMARY KEY, value TEXT NOT NULL);
