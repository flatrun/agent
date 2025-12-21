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

func (m *Manager) RevokePrivileges(cfg *ConnectionConfig, username, database string) error {
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
		query = fmt.Sprintf("REVOKE ALL PRIVILEGES ON `%s`.* FROM '%s'@'%%'", database, username)
	case "postgresql":
		query = fmt.Sprintf("REVOKE ALL PRIVILEGES ON DATABASE \"%s\" FROM %s", database, username)
	}

	_, err = db.Exec(query)
	return err
}

func (m *Manager) DropDatabase(cfg *ConnectionConfig, dbName string) error {
	return m.DeleteDatabase(cfg, dbName)
}

func (m *Manager) DropUser(cfg *ConnectionConfig, username string) error {
	return m.DeleteUser(cfg, username, "%")
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

type ColumnSchema struct {
	Name     string      `json:"name"`
	Type     string      `json:"type"`
	Nullable bool        `json:"nullable"`
	Default  interface{} `json:"default"`
	Key      string      `json:"key"`
	Extra    string      `json:"extra"`
}

type IndexSchema struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique"`
	Primary bool     `json:"primary"`
}

type TableSchema struct {
	Columns []ColumnSchema `json:"columns"`
	Indexes []IndexSchema  `json:"indexes"`
}

func (m *Manager) DescribeTable(cfg *ConnectionConfig, database, table string) (*TableSchema, error) {
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

	table = strings.ReplaceAll(table, "`", "")
	table = strings.ReplaceAll(table, "'", "")
	table = strings.ReplaceAll(table, "\"", "")
	table = strings.ReplaceAll(table, ";", "")

	schema := &TableSchema{
		Columns: []ColumnSchema{},
		Indexes: []IndexSchema{},
	}

	switch cfg.Type {
	case "mysql", "mariadb":
		if err := m.describeMySQLTable(db, table, schema); err != nil {
			return nil, err
		}
	case "postgresql":
		if err := m.describePostgresTable(db, table, schema); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	return schema, nil
}

func (m *Manager) describeMySQLTable(db *sql.DB, table string, schema *TableSchema) error {
	rows, err := db.Query(fmt.Sprintf("DESCRIBE `%s`", table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var field, colType, null, key string
		var defaultVal, extra sql.NullString

		if err := rows.Scan(&field, &colType, &null, &key, &defaultVal, &extra); err != nil {
			continue
		}

		col := ColumnSchema{
			Name:     field,
			Type:     colType,
			Nullable: null == "YES",
			Key:      key,
			Extra:    extra.String,
		}
		if defaultVal.Valid {
			col.Default = defaultVal.String
		}
		schema.Columns = append(schema.Columns, col)
	}

	indexRows, err := db.Query(fmt.Sprintf("SHOW INDEX FROM `%s`", table))
	if err != nil {
		return nil
	}
	defer indexRows.Close()

	indexMap := make(map[string]*IndexSchema)
	for indexRows.Next() {
		var tableName, keyName, columnName string
		var nonUnique int
		var seqInIndex, cardinality sql.NullInt64
		var collation, subPart, packed, null, indexType, comment, indexComment sql.NullString
		var visible sql.NullString

		cols, _ := indexRows.Columns()
		var scanArgs []interface{}
		if len(cols) >= 15 {
			scanArgs = []interface{}{&tableName, &nonUnique, &keyName, &seqInIndex, &columnName,
				&collation, &cardinality, &subPart, &packed, &null, &indexType, &comment, &indexComment, &visible}
			if len(cols) > 14 {
				var extra sql.NullString
				scanArgs = append(scanArgs, &extra)
			}
		} else {
			scanArgs = []interface{}{&tableName, &nonUnique, &keyName, &seqInIndex, &columnName,
				&collation, &cardinality, &subPart, &packed, &null, &indexType, &comment, &indexComment}
		}

		if err := indexRows.Scan(scanArgs[:len(cols)]...); err != nil {
			continue
		}

		if _, exists := indexMap[keyName]; !exists {
			indexMap[keyName] = &IndexSchema{
				Name:    keyName,
				Columns: []string{},
				Unique:  nonUnique == 0,
				Primary: keyName == "PRIMARY",
			}
		}
		indexMap[keyName].Columns = append(indexMap[keyName].Columns, columnName)
	}

	for _, idx := range indexMap {
		schema.Indexes = append(schema.Indexes, *idx)
	}

	return nil
}

func (m *Manager) describePostgresTable(db *sql.DB, table string, schema *TableSchema) error {
	query := `
		SELECT
			c.column_name,
			c.data_type || COALESCE('(' || c.character_maximum_length::text || ')', '') as full_type,
			c.is_nullable,
			c.column_default,
			CASE
				WHEN pk.column_name IS NOT NULL THEN 'PRI'
				WHEN uq.column_name IS NOT NULL THEN 'UNI'
				ELSE ''
			END as key_type
		FROM information_schema.columns c
		LEFT JOIN (
			SELECT kcu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
				ON tc.constraint_name = kcu.constraint_name
				AND tc.table_schema = kcu.table_schema
			WHERE tc.constraint_type = 'PRIMARY KEY'
				AND tc.table_name = $1
		) pk ON c.column_name = pk.column_name
		LEFT JOIN (
			SELECT kcu.column_name
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
				ON tc.constraint_name = kcu.constraint_name
				AND tc.table_schema = kcu.table_schema
			WHERE tc.constraint_type = 'UNIQUE'
				AND tc.table_name = $1
		) uq ON c.column_name = uq.column_name
		WHERE c.table_name = $1
		ORDER BY c.ordinal_position
	`

	rows, err := db.Query(query, table)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var name, colType, nullable, key string
		var defaultVal sql.NullString

		if err := rows.Scan(&name, &colType, &nullable, &defaultVal, &key); err != nil {
			continue
		}

		col := ColumnSchema{
			Name:     name,
			Type:     colType,
			Nullable: nullable == "YES",
			Key:      key,
			Extra:    "",
		}
		if defaultVal.Valid {
			col.Default = defaultVal.String
		}
		schema.Columns = append(schema.Columns, col)
	}

	indexQuery := `
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE tablename = $1
	`
	indexRows, err := db.Query(indexQuery, table)
	if err != nil {
		return nil
	}
	defer indexRows.Close()

	for indexRows.Next() {
		var indexName, indexDef string
		if err := indexRows.Scan(&indexName, &indexDef); err != nil {
			continue
		}

		idx := IndexSchema{
			Name:    indexName,
			Columns: []string{},
			Unique:  strings.Contains(indexDef, "UNIQUE"),
			Primary: strings.HasSuffix(indexName, "_pkey"),
		}

		start := strings.Index(indexDef, "(")
		end := strings.LastIndex(indexDef, ")")
		if start != -1 && end != -1 && end > start {
			colStr := indexDef[start+1 : end]
			cols := strings.Split(colStr, ",")
			for _, c := range cols {
				idx.Columns = append(idx.Columns, strings.TrimSpace(c))
			}
		}

		schema.Indexes = append(schema.Indexes, idx)
	}

	return nil
}
