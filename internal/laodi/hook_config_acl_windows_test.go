//go:build windows

package laodi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWindowsHookClientACLAllowDenyAndInheritance(t *testing.T) {
	sid, err := windowsCurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	const foreign = "S-1-5-21-100-200-300-44444"
	for _, tc := range []struct {
		name, ace, role, permissions, scope, inheritance string
		directory                                        bool
	}{
		{name: "foreign_read_only", ace: "(A;;GR;;;" + foreign + ")"},
		{name: "foreign_deny", ace: "(D;;FA;;;" + foreign + ")"},
		{name: "directory_foreign_deny", ace: "(D;OICI;FA;;;" + foreign + ")", directory: true},
		{name: "deny_does_not_cancel_write_allow", ace: "(D;;0x2;;;" + foreign + ")(A;;0x2;;;" + foreign + ")", role: "other_principal", permissions: "data_write", scope: "object"},
		{name: "foreign_data_write", ace: "(A;;0x2;;;" + foreign + ")", role: "other_principal", permissions: "data_write", scope: "object"},
		{name: "everyone_delete", ace: "(A;;0x10000;;;WD)", role: "everyone", permissions: "delete", scope: "object"},
		{name: "authenticated_security_write", ace: "(A;;0x40000;;;AU)", role: "authenticated_users", permissions: "security_write", scope: "object"},
		{name: "builtin_users_metadata_write", ace: "(A;;0x100;;;BU)", role: "builtin_users", permissions: "metadata_write", scope: "object"},
		{name: "inherited_builtin_users_write", ace: "(A;ID;0x2;;;BU)", role: "builtin_users", permissions: "data_write", scope: "object", inheritance: "inherited"},
		{name: "file_inherit_only_cannot_grant_access", ace: "(A;OIIO;0x2;;;" + foreign + ")"},
		{name: "directory_inherit_only_grants_children", ace: "(A;OICIIO;0x2;;;" + foreign + ")", directory: true, role: "other_principal", permissions: "data_write", scope: "descendants", inheritance: "files+directories+inherit_only"},
		{name: "directory_creator_owner_inherit_only", ace: "(A;OICIIO;FA;;;CO)", directory: true},
		{name: "directory_unpropagated_inherit_only", ace: "(A;IO;0x2;;;" + foreign + ")", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateStateDir(t)
			path := filepath.Join(root, "synthetic-config")
			if tc.directory {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			// Only this newly created fixture receives a DACL. No profile,
			// existing client settings or real Startup permissions are changed.
			setStartupTestDACL(t, path, "D:P"+tc.ace+"(A;;FA;;;"+sid+")(A;;FA;;;SY)(A;;FA;;;BA)")
			before := startupTestDACL(t, path)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			err = windowsCheckClientHandle(syscall.Handle(f.Fd()), tc.directory)
			f.Close()
			if after := startupTestDACL(t, path); after != before {
				t.Fatal("ACL inspection modified fixture permissions")
			}
			if tc.role == "" {
				if err != nil {
					t.Fatalf("non-granting ACE rejected: %v", err)
				}
				return
			}
			var diagnostic *windowsHookACLError
			if !errors.As(err, &diagnostic) || diagnostic.Reason != "foreign_write_allow" || diagnostic.Role != tc.role || diagnostic.Permissions != tc.permissions || diagnostic.Scope != tc.scope || diagnostic.Entry != "allow" {
				t.Fatalf("missing safe ACL classification: %v", err)
			}
			inheritance := tc.inheritance
			if inheritance == "" {
				inheritance = "none"
			}
			if diagnostic.Inheritance != inheritance {
				t.Fatalf("wrong ACL inheritance category: %v", err)
			}
			for _, private := range []string{sid, foreign, root, path, "S-1-", "synthetic-config"} {
				if strings.Contains(err.Error(), private) {
					t.Fatal("ACL diagnostic disclosed identity or path")
				}
			}
		})
	}
}

func TestWindowsHookClientACLDiagnosticStages(t *testing.T) {
	sid, err := windowsCurrentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"home_directory", "ancestor_directory", "settings_file"} {
		t.Run(stage, func(t *testing.T) {
			home := privateStateDir(t)
			parent := filepath.Join(home, "client")
			path := filepath.Join(parent, "settings.json")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			target := map[string]string{"home_directory": home, "ancestor_directory": parent, "settings_file": path}[stage]
			setStartupTestDACL(t, target, "D:P(A;;FA;;;"+sid+")(A;;FA;;;SY)(A;;0x2;;;BU)")
			var diagnostic *windowsHookACLError
			if err := checkHookPath(home, path); !errors.As(err, &diagnostic) || diagnostic.Stage != stage || diagnostic.Role != "builtin_users" {
				t.Fatalf("wrong ACL check stage: %v", err)
			}
		})
	}
}
