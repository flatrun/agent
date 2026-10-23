package scheduler

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestUpgradedDatabaseAcceptsEveryTaskType(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, ".flatrun")
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		t.Fatal(err)
	}
	old, err := sql.Open("sqlite", "file:"+filepath.Join(dbDir, "scheduler.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`
	CREATE TABLE scheduled_tasks (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL,
		type TEXT NOT NULL CHECK (type IN ('backup', 'command')),
		deployment_name TEXT NOT NULL,
		cron_expr TEXT NOT NULL,
		enabled BOOLEAN DEFAULT TRUE,
		config TEXT,
		last_run DATETIME,
		next_run DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	INSERT INTO scheduled_tasks (name, type, deployment_name, cron_expr, config) VALUES ('nightly', 'backup', 'shop', '@daily', '{}');`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	m, err := NewManager(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := m.GetAllTasks()
	if err != nil || len(tasks) != 1 || tasks[0].Name != "nightly" {
		t.Fatalf("existing tasks = %+v, %v", tasks, err)
	}
	if err := m.SyncAgentTask("triage", "@hourly", "shop"); err != nil {
		t.Fatalf("agent task: %v", err)
	}
	if err := m.SyncRotationTask("deployment:shop", "TOKEN", "@weekly", "shop"); err != nil {
		t.Fatalf("rotation task: %v", err)
	}
	if _, err := m.CreateTask(&CreateTaskRequest{Name: "x", Type: "nonsense", DeploymentName: "shop", CronExpr: "@daily"}); err == nil {
		t.Fatal("an unknown task type was accepted")
	}
	if tasks, _ := m.GetAllTasks(); len(tasks) != 3 {
		t.Fatalf("tasks after upgrade = %d", len(tasks))
	}
}
