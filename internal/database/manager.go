package database

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

type networkInfo struct {
	IPAddress string `json:"IPAddress"`
}

// parseContainerIP extracts IP from docker network JSON, preferring "database" network
func parseContainerIP(jsonOutput []byte) string {
	var networks map[string]networkInfo
	if err := json.Unmarshal(jsonOutput, &networks); err != nil {
		return ""
	}

	if net, ok := networks["database"]; ok && net.IPAddress != "" {
		return net.IPAddress
	}

	for _, net := range networks {
		if net.IPAddress != "" {
			return net.IPAddress
		}
	}

	return ""
}

type ConnectionConfig struct {
	Type      string `json:"type"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Database  string `json:"database,omitempty"`
	Container string `json:"container,omitempty"`
}

type DatabaseInfo struct {
	Name   string `json:"name"`
	Size   string `json:"size,omitempty"`
	Tables int    `json:"tables,omitempty"`
}

type TableInfo struct {
	Name   string `json:"name"`
	Rows   int64  `json:"rows,omitempty"`
	Size   string `json:"size,omitempty"`
	Engine string `json:"engine,omitempty"`
}

type UserInfo struct {
	Name string `json:"name"`
	Host string `json:"host,omitempty"`
}

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) resolveContainerConnection(cfg *ConnectionConfig) (string, int, error) {
	containerName := cfg.Container
	if containerName == "" {
		containerName = cfg.Host
	}
	if containerName == "" {
		return "localhost", cfg.Port, nil
	}

	// Skip resolution for IP addresses and localhost
	if containerName == "localhost" || containerName == "127.0.0.1" ||
		strings.HasPrefix(containerName, "192.168.") ||
		strings.HasPrefix(containerName, "10.") ||
		strings.HasPrefix(containerName, "172.") {
		return containerName, cfg.Port, nil
	}

	cmd := exec.Command("docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", containerName)
	output, err := cmd.Output()
	if err != nil {
		host := cfg.Host
		if host == "" {
			host = "localhost"
		}
		return host, cfg.Port, nil
	}

	ip := parseContainerIP(output)
	if ip == "" {
		return cfg.Host, cfg.Port, nil
	}

	port := cfg.Port
	portCmd := exec.Command("docker", "inspect", "--format", "{{range $p, $conf := .Config.ExposedPorts}}{{$p}} {{end}}", containerName)
	portOutput, err := portCmd.Output()
	if err == nil {
		ports := strings.Fields(string(portOutput))
		for _, p := range ports {
			parts := strings.Split(p, "/")
			if len(parts) > 0 {
				if parsedPort := parsePort(parts[0]); parsedPort > 0 {
					if isDbPort(parsedPort, cfg.Type) {
						port = parsedPort
						break
					}
				}
			}
		}
	}

	return ip, port, nil
}

func parsePort(s string) int {
	var port int
	_, _ = fmt.Sscanf(s, "%d", &port)
	return port
}

func isDbPort(port int, dbType string) bool {
	switch dbType {
	case "mysql", "mariadb":
		return port == 3306
	case "postgresql":
		return port == 5432
	case "mongodb":
		return port == 27017
	case "redis":
		return port == 6379
	}
	return false
}

func (m *Manager) buildDSN(cfg *ConnectionConfig) (string, error) {
	host, port, _ := m.resolveContainerConnection(cfg)

	switch cfg.Type {
	case "mysql", "mariadb":
		db := cfg.Database
		if db == "" {
			db = "information_schema"
		}
		return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s",
			cfg.Username, cfg.Password, host, port, db), nil
	case "postgresql":
		sslmode := "disable"
		db := cfg.Database
		if db == "" {
			db = "postgres"
		}
		return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			host, port, cfg.Username, cfg.Password, db, sslmode), nil
	default:
		return "", fmt.Errorf("unsupported database type: %s", cfg.Type)
	}
}

func (m *Manager) getDriver(dbType string) string {
	switch dbType {
	case "mysql", "mariadb":
		return "mysql"
	case "postgresql":
		return "postgres"
	default:
		return ""
	}
}

func (m *Manager) TestConnection(cfg *ConnectionConfig) error {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return fmt.Errorf("failed to open connection: %w", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	return nil
}

func (m *Manager) ListDatabases(cfg *ConnectionConfig) ([]DatabaseInfo, error) {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		query = "SHOW DATABASES"
	case "postgresql":
		query = "SELECT datname FROM pg_database WHERE datistemplate = false"
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var databases []DatabaseInfo
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		if name == "information_schema" || name == "performance_schema" ||
			name == "mysql" || name == "sys" {
			continue
		}
		databases = append(databases, DatabaseInfo{Name: name})
	}

	return databases, nil
}

func (m *Manager) ListTables(cfg *ConnectionConfig, database string) ([]TableInfo, error) {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	cfgCopy := *cfg
	cfgCopy.Database = database

	dsn, err := m.buildDSN(&cfgCopy)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		query = fmt.Sprintf(`
			SELECT TABLE_NAME, TABLE_ROWS, ENGINE
			FROM information_schema.TABLES
			WHERE TABLE_SCHEMA = '%s'`, database)
	case "postgresql":
		query = `
			SELECT tablename, 0, 'postgres'
			FROM pg_tables
			WHERE schemaname = 'public'`
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var t TableInfo
		var rowCount sql.NullInt64
		var engine sql.NullString
		if err := rows.Scan(&t.Name, &rowCount, &engine); err != nil {
			continue
		}
		t.Rows = rowCount.Int64
		t.Engine = engine.String
		tables = append(tables, t)
	}

	return tables, nil
}

func (m *Manager) ListUsers(cfg *ConnectionConfig) ([]UserInfo, error) {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		query = "SELECT User, Host FROM mysql.user"
	case "postgresql":
		query = "SELECT usename, '' FROM pg_user"
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []UserInfo
	for rows.Next() {
		var u UserInfo
		if err := rows.Scan(&u.Name, &u.Host); err != nil {
			continue
		}
		users = append(users, u)
	}

	return users, nil
}

func (m *Manager) CreateDatabase(cfg *ConnectionConfig, dbName string) error {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	dbName = strings.ReplaceAll(dbName, "`", "")
	dbName = strings.ReplaceAll(dbName, "'", "")
	dbName = strings.ReplaceAll(dbName, "\"", "")

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		query = fmt.Sprintf("CREATE DATABASE `%s`", dbName)
	case "postgresql":
		query = fmt.Sprintf("CREATE DATABASE \"%s\"", dbName)
	}

	_, err = db.Exec(query)
	return err
}

func (m *Manager) CreateUser(cfg *ConnectionConfig, username, password, host string) error {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		if host == "" {
			host = "%"
		}
		query = fmt.Sprintf("CREATE USER '%s'@'%s' IDENTIFIED BY '%s'", username, host, password)
	case "postgresql":
		query = fmt.Sprintf("CREATE USER %s WITH PASSWORD '%s'", username, password)
	}

	_, err = db.Exec(query)
	return err
}

func (m *Manager) GrantPrivileges(cfg *ConnectionConfig, username, database, host string) error {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		if host == "" {
			host = "%"
		}
		query = fmt.Sprintf("GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s'", database, username, host)
	case "postgresql":
		query = fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE \"%s\" TO %s", database, username)
	}

	_, err = db.Exec(query)
	return err
}

func (m *Manager) DeleteDatabase(cfg *ConnectionConfig, dbName string) error {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	dbName = strings.ReplaceAll(dbName, "`", "")
	dbName = strings.ReplaceAll(dbName, "'", "")
	dbName = strings.ReplaceAll(dbName, "\"", "")

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		query = fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", dbName)
	case "postgresql":
		query = fmt.Sprintf("DROP DATABASE IF EXISTS \"%s\"", dbName)
	}

	_, err = db.Exec(query)
	return err
}

func (m *Manager) DeleteUser(cfg *ConnectionConfig, username, host string) error {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	dsn, err := m.buildDSN(cfg)
	if err != nil {
		return err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		if host == "" {
			host = "%"
		}
		query = fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s'", username, host)
	case "postgresql":
		query = fmt.Sprintf("DROP USER IF EXISTS %s", username)
	}

	_, err = db.Exec(query)
	return err
}

type QueryResult struct {
	Columns []string        `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	Count   int             `json:"count"`
}

func (m *Manager) QueryTable(cfg *ConnectionConfig, database, table string, limit, offset int) (*QueryResult, error) {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	cfgCopy := *cfg
	cfgCopy.Database = database

	dsn, err := m.buildDSN(&cfgCopy)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Sanitize table name
	table = strings.ReplaceAll(table, "`", "")
	table = strings.ReplaceAll(table, "'", "")
	table = strings.ReplaceAll(table, "\"", "")
	table = strings.ReplaceAll(table, ";", "")

	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}

	var query string
	switch cfg.Type {
	case "mysql", "mariadb":
		query = fmt.Sprintf("SELECT * FROM `%s` LIMIT %d OFFSET %d", table, limit, offset)
	case "postgresql":
		query = fmt.Sprintf("SELECT * FROM \"%s\" LIMIT %d OFFSET %d", table, limit, offset)
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{
		Columns: columns,
		Rows:    [][]interface{}{},
	}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		row := make([]interface{}, len(columns))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case nil:
				row[i] = nil
			default:
				row[i] = val
			}
		}
		result.Rows = append(result.Rows, row)
	}

	result.Count = len(result.Rows)
	return result, nil
}

func (m *Manager) ExecuteQuery(cfg *ConnectionConfig, database, query string) (*QueryResult, error) {
	driver := m.getDriver(cfg.Type)
	if driver == "" {
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	cfgCopy := *cfg
	cfgCopy.Database = database

	dsn, err := m.buildDSN(&cfgCopy)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Basic safety: only allow SELECT queries
	trimmedQuery := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(trimmedQuery, "SELECT") &&
		!strings.HasPrefix(trimmedQuery, "SHOW") &&
		!strings.HasPrefix(trimmedQuery, "DESCRIBE") &&
		!strings.HasPrefix(trimmedQuery, "EXPLAIN") {
		return nil, fmt.Errorf("only SELECT, SHOW, DESCRIBE, and EXPLAIN queries are allowed")
	}

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{
		Columns: columns,
		Rows:    [][]interface{}{},
	}

	for rows.Next() {
		values := make([]interface{}, len(columns))
		valuePtrs := make([]interface{}, len(columns))
		for i := range values {
			valuePtrs[i] = &values[i]
		}

		if err := rows.Scan(valuePtrs...); err != nil {
			continue
		}

		row := make([]interface{}, len(columns))
		for i, v := range values {
			switch val := v.(type) {
			case []byte:
				row[i] = string(val)
			case nil:
				row[i] = nil
			default:
				row[i] = val
			}
		}
		result.Rows = append(result.Rows, row)
	}

	result.Count = len(result.Rows)
	return result, nil
}
