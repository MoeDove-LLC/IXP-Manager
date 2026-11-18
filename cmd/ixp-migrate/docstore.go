package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/migrate"
)

// docstoreDiskRoots maps explicit legacy disk aliases to offline directories.
// It never resolves Laravel configuration, environment variables, or URLs.
type docstoreDiskRoots map[string]string

func (d *docstoreDiskRoots) String() string { return "explicit offline disk roots" }
func (d *docstoreDiskRoots) Set(value string) error {
	disk, root, ok := strings.Cut(value, "=")
	if !ok || disk == "" || root == "" || len(disk) > 128 || strings.TrimSpace(disk) != disk || strings.ContainsAny(disk, "/\\\x00\n\r\t") {
		return fmt.Errorf("--docstore-disk-root requires disk=/explicit/offline/root")
	}
	if *d == nil {
		*d = docstoreDiskRoots{}
	}
	if _, exists := (*d)[disk]; exists {
		return fmt.Errorf("duplicate explicit docstore disk root %q", disk)
	}
	(*d)[disk] = root
	return nil
}

func docstoreSourceRef(kind, entity string, value any) string {
	n, err := integer(value)
	if err != nil || n < 1 {
		return ""
	}
	return migrate.DocstoreID(kind, entity, n)
}

func (c *converter) docstore() error {
	tables := []string{"docstore_directories", "docstore_customer_directories", "docstore_files", "docstore_customer_files", "docstore_logs"}
	total := 0
	for _, table := range tables {
		total += len(c.source.Tables[table])
	}
	if total == 0 {
		return nil
	}
	p := &migrate.DocstoreImport{Version: 1, SourceTimezone: "unknown", Directories: []migrate.DocstoreDirectorySource{}, Files: []migrate.DocstoreFileSource{}, Logs: []migrate.DocstoreLogSource{}}
	for _, kind := range []string{"shared", "customer"} {
		table := "docstore_directories"
		fields := []string{"id", "parent_dir_id", "name", "description", "created_at", "updated_at"}
		if kind == "customer" {
			table = "docstore_customer_directories"
			fields = append(fields, "cust_id")
		}
		for _, r := range c.source.Tables[table] {
			facts, err := c.patchFacts(table, r, fields)
			if err != nil {
				return err
			}
			member := ""
			if kind == "customer" {
				member = c.ref(table, r, "cust_id", "members")
			}
			p.Directories = append(p.Directories, migrate.DocstoreDirectorySource{ID: migrate.DocstoreID(kind, "directory", idOf(r)), LegacyID: idOf(r), Kind: kind, ParentID: docstoreSourceRef(kind, "directory", r["parent_dir_id"]), MemberID: member, Name: facts["name"], Description: facts["description"], Facts: facts})
		}
		table, directory := "docstore_files", "docstore_directory_id"
		if kind == "customer" {
			table, directory = "docstore_customer_files", "docstore_customer_directory_id"
		}
		fields = []string{"id", directory, "name", "description", "disk", "path", "sha256", "min_privs", "file_last_updated", "created_by", "created_at", "updated_at"}
		if kind == "customer" {
			fields = append(fields, "cust_id")
		}
		for _, r := range c.source.Tables[table] {
			facts, err := c.patchFacts(table, r, fields)
			if err != nil {
				return err
			}
			member := ""
			if kind == "customer" {
				member = c.ref(table, r, "cust_id", "members")
				if member == "" {
					c.warn(table, r, "source customer was not converted; document remains staff-review only")
				}
			}
			privilege := migrate.DocstorePrivilege(facts["min_privs"])
			if privilege == -1 {
				c.warn(table, r, "missing or unknown minimum privilege retained as staff-review only")
			}
			if facts["sha256"] == "" {
				c.warn(table, r, "source checksum absent; bundle records a newly calculated checksum, not proof of an original digest match")
			}
			p.Files = append(p.Files, migrate.DocstoreFileSource{ID: migrate.DocstoreID(kind, "file", idOf(r)), LegacyID: idOf(r), Kind: kind, DirectoryID: docstoreSourceRef(kind, "directory", r[directory]), MemberID: member, Name: facts["name"], Description: facts["description"], MinPrivileges: privilege, StorageKey: facts["path"], Entry: migrate.DocstoreFileEntry(kind, idOf(r)), Facts: facts})
		}
	}
	for _, r := range c.source.Tables["docstore_logs"] {
		facts, err := c.patchFacts("docstore_logs", r, []string{"id", "docstore_file_id", "downloaded_by", "created_at", "updated_at"})
		if err != nil {
			return err
		}
		var actor *int64
		if raw := facts["downloaded_by"]; raw != "" {
			n, e := strconv.ParseInt(raw, 10, 64)
			if e != nil || n < 1 {
				return fmt.Errorf("docstore_logs id %d: invalid legacy actor ID", idOf(r))
			}
			actor = &n
		}
		p.Logs = append(p.Logs, migrate.DocstoreLogSource{ID: migrate.DocstoreID("shared", "log", idOf(r)), LegacyID: idOf(r), FileID: docstoreSourceRef("shared", "file", r["docstore_file_id"]), DownloadedBy: actor, CreatedAt: facts["created_at"], UpdatedAt: facts["updated_at"], Facts: facts})
	}
	if err := migrate.ValidateDocstore(p, false); err != nil {
		return err
	}
	c.result.Docstore = p
	return nil
}
