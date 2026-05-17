package sysd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// auditLogDirPerm: dir holding the audit log. `0o700` so only the
	// owning user can list it. Audit log dir also receives the audit log
	// file (`0o600`); a permissive dir mode would leak the file's
	// existence (and indirectly the user's a2text activity) to other
	// accounts on a shared machine.
	auditLogDirPerm = 0o700
	// auditLogFilePerm: file mode for the audit log itself. Owner
	// read/write only. Audit content includes peer PID/UID of every IPC
	// connection — never widen this.
	auditLogFilePerm = 0o600
)

// AuditLogPath returns the on-disk path of the append-only security audit
// log. Honours XDG_DATA_HOME, falls back to `~/.local/share/<AppName>/audit.log`
// per the freedesktop Base Directory Specification.
//
// Format: one JSON object per line (slog JSON handler). Consumed
// post-incident with `jq` / `journalctl`-style grep — never read back by
// the daemon itself.
//
// Rationale for a separate file (vs the main slog stream): the operator
// can rotate it independently, ship it to a SIEM, and grant it different
// read perms without affecting noisy debug logs. The events written here
// are *security-relevant* only: IPC connection accepts, autopaste fires,
// refusals (race-guard, cross-UID rejects, permission denials).
//
// Returns an error only when $HOME is also unresolvable.
func AuditLogPath() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, AppName, "audit.log"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sysd: resolve $HOME for audit log: %w", err)
	}

	return filepath.Join(home, ".local", "share", AppName, "audit.log"), nil
}

// OpenAuditLog opens the audit log for append, creating it (and any
// missing parent directories) with `0o600` perms — root may read, no
// other UID can. The returned *os.File is owned by the caller; close
// during shutdown.
//
// Permission rationale: audit log contains PID/UID of every IPC peer
// plus timestamps of paste events. World/group read would leak the
// user's activity pattern to other accounts on a shared machine.
func OpenAuditLog() (*os.File, error) {
	path, err := AuditLogPath()
	if err != nil {
		return nil, err
	}

	if mkErr := os.MkdirAll(filepath.Dir(path), auditLogDirPerm); mkErr != nil {
		return nil, fmt.Errorf("sysd: create audit log dir: %w", mkErr)
	}

	file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_APPEND, auditLogFilePerm)
	if err != nil {
		return nil, fmt.Errorf("sysd: open audit log %q: %w", path, err)
	}

	return file, nil
}
