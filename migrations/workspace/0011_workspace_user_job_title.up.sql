-- Job title (docs/org-structure.md §3.3): the employee's function — "QA
-- engineer", "разработчик". The platform role (admin/operator/viewer) is about
-- system permissions; the job title is about who the person is, and it is the
-- natural address for group asks ("позови всех тестировщиков") through the
-- title: recipient.
ALTER TABLE workspace_user ADD COLUMN IF NOT EXISTS job_title text NOT NULL DEFAULT ''::text;
