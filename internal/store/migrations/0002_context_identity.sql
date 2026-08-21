ALTER TABLE projects ADD COLUMN kube_server TEXT NOT NULL DEFAULT '';
ALTER TABLE projects ADD COLUMN switch_prompt_suppressed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE project_workloads ADD COLUMN origin_context TEXT NOT NULL DEFAULT '';
ALTER TABLE project_workloads ADD COLUMN origin_server TEXT NOT NULL DEFAULT '';
ALTER TABLE project_resources ADD COLUMN origin_context TEXT NOT NULL DEFAULT '';
ALTER TABLE project_resources ADD COLUMN origin_server TEXT NOT NULL DEFAULT '';
