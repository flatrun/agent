package security

import (
	"regexp"
	"strings"
)

// Suspicious paths that are commonly probed by attackers
var SuspiciousPaths = []string{
	// WordPress
	"/wp-admin",
	"/wp-login.php",
	"/wp-config.php",
	"/wp-content/uploads",
	"/wp-includes",
	"/xmlrpc.php",
	"/wp-json/wp/v2/users",

	// Environment and config files
	"/.env",
	"/.env.local",
	"/.env.production",
	"/.env.backup",
	"/config.php",
	"/configuration.php",
	"/settings.php",
	"/local.xml",
	"/app/etc/local.xml",

	// Version control
	"/.git",
	"/.git/config",
	"/.git/HEAD",
	"/.gitignore",
	"/.svn",
	"/.hg",

	// Package managers
	"/composer.json",
	"/composer.lock",
	"/package.json",
	"/yarn.lock",
	"/Gemfile",
	"/requirements.txt",

	// Database tools
	"/phpMyAdmin",
	"/phpmyadmin",
	"/pma",
	"/myadmin",
	"/mysql",
	"/adminer.php",
	"/adminer",

	// Admin panels
	"/admin",
	"/administrator",
	"/admin.php",
	"/manager",
	"/cpanel",
	"/webadmin",
	"/controlpanel",

	// Shell/backdoor paths
	"/shell",
	"/cmd",
	"/c99",
	"/r57",
	"/shell.php",
	"/cmd.php",
	"/backdoor",
	"/webshell",

	// Cloud/Infrastructure
	"/.aws",
	"/.aws/credentials",
	"/.ssh",
	"/.ssh/id_rsa",
	"/.docker",
	"/docker-compose.yml",
	"/.kube/config",

	// API/Debug endpoints
	"/actuator",
	"/actuator/env",
	"/actuator/health",
	"/api/swagger",
	"/swagger.json",
	"/swagger-ui",
	"/api-docs",
	"/.well-known/security.txt",
	"/debug",
	"/trace",
	"/server-status",
	"/server-info",

	// Backup files
	"/.bak",
	"/.backup",
	"/.old",
	"/backup.sql",
	"/database.sql",
	"/dump.sql",
	"/db.sql",

	// Laravel specific
	"/storage/logs",
	"/.env.example",
	"/artisan",

	// Common CMS
	"/sites/default/settings.php",
	"/typo3conf",
	"/fileadmin",
	"/magento",

	// Sensitive files
	"/.htaccess",
	"/.htpasswd",
	"/web.config",
	"/crossdomain.xml",
	"/clientaccesspolicy.xml",
}

// Scanner user agent patterns
var ScannerUserAgents = []string{
	"nikto",
	"nmap",
	"sqlmap",
	"dirbuster",
	"dirb",
	"gobuster",
	"nuclei",
	"masscan",
	"wpscan",
	"burp",
	"acunetix",
	"nessus",
	"openvas",
	"w3af",
	"arachni",
	"skipfish",
	"whatweb",
	"joomscan",
	"droopescan",
	"zgrab",
	"curl/",
	"wget/",
	"python-requests",
	"go-http-client",
	"httpx",
	"nuclei",
}

var suspiciousPathPatterns []*regexp.Regexp

func init() {
	patterns := []string{
		`(?i)\.(env|bak|backup|old|orig|copy|tmp|temp|swp|save)$`,
		`(?i)\.(sql|db|sqlite|sqlite3|mdb)$`,
		`(?i)\.(zip|tar|gz|rar|7z)$`,
		`(?i)/(\.git|\.svn|\.hg)/`,
		`(?i)/wp-(admin|login|config|includes)/`,
		`(?i)/admin(istrator)?/`,
		`(?i)/php(my)?admin/`,
		`(?i)/(shell|cmd|backdoor|webshell)/`,
		`(?i)\.(php|asp|aspx|jsp|cgi)\d*$`,
	}

	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			suspiciousPathPatterns = append(suspiciousPathPatterns, re)
		}
	}
}

// IsSuspiciousPath checks if a path is suspicious
func IsSuspiciousPath(path string) bool {
	pathLower := strings.ToLower(path)

	// Check exact matches
	for _, suspicious := range SuspiciousPaths {
		if strings.HasPrefix(pathLower, strings.ToLower(suspicious)) {
			return true
		}
	}

	// Check regex patterns
	for _, re := range suspiciousPathPatterns {
		if re.MatchString(path) {
			return true
		}
	}

	return false
}

// IsScanner checks if user agent indicates a scanner
func IsScanner(userAgent string) bool {
	if userAgent == "" {
		return false
	}

	userAgentLower := strings.ToLower(userAgent)
	for _, scanner := range ScannerUserAgents {
		if strings.Contains(userAgentLower, scanner) {
			return true
		}
	}

	return false
}

// GetSuspiciousPathDescription returns a description for why a path is suspicious
func GetSuspiciousPathDescription(path string) string {
	pathLower := strings.ToLower(path)

	switch {
	case strings.Contains(pathLower, "wp-"):
		return "WordPress probe"
	case strings.Contains(pathLower, ".env"):
		return "Environment file probe"
	case strings.Contains(pathLower, ".git"):
		return "Git repository probe"
	case strings.Contains(pathLower, "admin"):
		return "Admin panel probe"
	case strings.Contains(pathLower, "phpmyadmin") || strings.Contains(pathLower, "pma"):
		return "Database admin probe"
	case strings.Contains(pathLower, "shell") || strings.Contains(pathLower, "cmd"):
		return "Shell/backdoor probe"
	case strings.Contains(pathLower, ".sql") || strings.Contains(pathLower, "dump"):
		return "Database dump probe"
	case strings.Contains(pathLower, "config") || strings.Contains(pathLower, "settings"):
		return "Configuration file probe"
	case strings.Contains(pathLower, ".bak") || strings.Contains(pathLower, "backup"):
		return "Backup file probe"
	case strings.Contains(pathLower, "actuator") || strings.Contains(pathLower, "swagger"):
		return "API endpoint probe"
	default:
		return "Suspicious path probe"
	}
}
