package sudoterm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuditAppendsPrivateLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tmuxatlas", "sudo-audit.jsonl")
	audit, err := NewAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	entries := []AuditEntry{
		{Event: "open", RequestID: "sudo-1", Version: 2, Command: "apt-get install -y jq", Approver: "admin"},
		{Event: "finished", RequestID: "sudo-1", Version: 2, Command: "apt-get install -y jq", ExitCode: &code, OutputBytes: 42},
	}
	for _, entry := range entries {
		if err := audit.Append(entry); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", info.Mode(), err)
	}
	file, _ := os.Open(path)
	defer file.Close()
	read, err := ReadAudit(file, 10)
	if err != nil || len(read) != 2 {
		t.Fatalf("ReadAudit = %+v, %v", read, err)
	}
	if read[0].CommandSHA256 != CommandDigest("apt-get install -y jq") || read[1].ExitCode == nil || read[0].Time.IsZero() {
		t.Fatalf("entries = %+v", read)
	}
}
