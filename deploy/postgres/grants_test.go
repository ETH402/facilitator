package postgres

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestEveryApplicationTableHasAnExplicitRuntimeGrant(t *testing.T) {
	t.Parallel()

	tables := migrationTables(t)

	grantContents, err := os.ReadFile("grant-runtime.sql")
	if err != nil {
		t.Fatal(err)
	}
	grantSQL := string(grantContents)
	var missing []string
	for table := range tables {
		if !regexp.MustCompile(`(?m)\b` + regexp.QuoteMeta(table) + `\b`).MatchString(grantSQL) {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("tables without an explicit runtime grant: %s", strings.Join(missing, ", "))
	}
}

func TestExistingSchemaAdoptionIsExplicitAndComplete(t *testing.T) {
	t.Parallel()
	tables := migrationTables(t)
	contents, err := os.ReadFile("adopt-existing-schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	adoptionSQL := string(contents)
	var missing []string
	for table := range tables {
		if table == "email_delivery_outbox" {
			continue
		}
		if !regexp.MustCompile(`(?m)^ALTER TABLE\s+` + regexp.QuoteMeta(table) + `\s+OWNER TO eth402_migration;`).MatchString(adoptionSQL) {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Fatalf("tables missing from existing-schema adoption: %s", strings.Join(missing, ", "))
	}
	if regexp.MustCompile(`(?im)^\s*REASSIGN\s+OWNED\b`).MatchString(adoptionSQL) {
		t.Fatal("existing-schema adoption uses broad REASSIGN OWNED")
	}
	for _, required := range []string{
		"BEGIN;",
		"COMMIT;",
		"000012_public_merchant_profiles",
		"000013_email_delivery_outbox",
		"to_regclass('public.email_delivery_outbox')",
		"ALTER TABLE email_delivery_outbox OWNER TO eth402_migration",
	} {
		if !strings.Contains(adoptionSQL, required) {
			t.Fatalf("existing-schema adoption is missing %q", required)
		}
	}
}

func migrationTables(t *testing.T) map[string]bool {
	t.Helper()
	migrations, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	createTable := regexp.MustCompile(`(?im)^CREATE TABLE\s+([a-z_][a-z_0-9]*)\s*\(`)
	tables := map[string]bool{"schema_migrations": true}
	for _, migration := range migrations {
		// #nosec G304 -- every path comes from the fixed repository migration glob.
		contents, readErr := os.ReadFile(migration)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, match := range createTable.FindAllStringSubmatch(string(contents), -1) {
			tables[match[1]] = true
		}
	}
	return tables
}

func TestRuntimeGrantDoesNotPermitDDLOrBroadPrivileges(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("grant-runtime.sql")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.ToUpper(string(contents))
	for _, forbidden := range []string{
		"GRANT ALL",
		"GRANT CREATE",
		"GRANT TRIGGER",
		"GRANT TRUNCATE",
		"ALTER DEFAULT PRIVILEGES",
		"ON ALL TABLES",
		"ON ALL SEQUENCES",
	} {
		if strings.Contains(normalized, forbidden) {
			t.Fatalf("runtime grant contains forbidden privilege form %q", forbidden)
		}
	}
}

func TestProvisioningScriptsAreTransactionalAndDatabaseBound(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"bootstrap-roles.sql",
		"adopt-existing-schema.sql",
		"grant-runtime.sql",
	} {
		// #nosec G304 -- name is selected only from the fixed literal list above.
		contents, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		sql := string(contents)
		if !regexp.MustCompile(`(?m)^BEGIN;$`).MatchString(sql) ||
			!regexp.MustCompile(`(?m)^COMMIT;$`).MatchString(sql) {
			t.Fatalf("%s is not explicitly transactional", name)
		}
		if !strings.Contains(sql, "current_database()") || !strings.Contains(sql, "'eth402'") {
			t.Fatalf("%s does not assert the target database", name)
		}
	}
}

func TestAppendOnlyTablesCannotBeUpdatedOrDeleted(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("grant-runtime.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	appendOnly := []string{
		"recipient_address_history",
		"verification_attempts",
		"settlement_attempts",
		"payment_transitions",
		"audit_events",
	}
	for _, table := range appendOnly {
		for _, privilege := range []string{"UPDATE", "DELETE"} {
			pattern := `(?is)GRANT\s+[^;]*\b` + privilege + `\b[^;]*\b` + regexp.QuoteMeta(table) + `\b[^;]*TO\s+eth402_runtime`
			if regexp.MustCompile(pattern).MatchString(sql) {
				t.Fatalf("append-only table %s is granted %s", table, privilege)
			}
		}
	}
}
