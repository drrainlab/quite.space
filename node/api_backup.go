package node

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Backup route. Registered from api.go.
//
// Only the WRITE side is exposed here. Restoring is deliberately a separate,
// offline act: a running node holds its own data directory open, and restoring
// into it would mean merging two histories. Pretending the app can restore
// itself would be a lie with a very delayed punchline.
func (a *APIServer) routeBackup(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/backup", a.auth(a.handleBackup))
	mux.HandleFunc("GET /api/backup/state", a.auth(a.handleBackupState))
}

// handleBackupState answers the one question the UI needs before it can nag
// usefully: has this person ever taken a backup from this device?
//
// It is device-local and approximate on purpose — it records that a backup was
// made, never where it was put. A node that tracked backup locations would be
// a node that knows where your keys live.
func (a *APIServer) handleBackupState(w http.ResponseWriter, r *http.Request) {
	last := a.rt.LastBackupAt()
	resp := map[string]any{"ever": last > 0, "last_at": last}
	if last > 0 {
		resp["age_days"] = int(time.Since(time.Unix(last, 0)).Hours() / 24)
	}
	writeJSON(w, resp)
}

func (a *APIServer) handleBackup(w http.ResponseWriter, r *http.Request) {
	body, err := readBody[struct {
		Passphrase string `json:"passphrase"`
	}](r)
	if err != nil || strings.TrimSpace(body.Passphrase) == "" {
		httpErr(w, http.StatusBadRequest, errors.New("a passphrase for the backup is required"))
		return
	}
	if len([]byte(body.Passphrase)) < 8 {
		httpErr(w, http.StatusBadRequest, ErrPassphraseTooShort)
		return
	}

	// Stream straight to the response: the backup is the size of the whole
	// data directory, and buffering it would mean holding a second copy of
	// somebody's entire history in memory.
	name := fmt.Sprintf("quiet-spaces-%s.qsbackup", time.Now().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := WriteBackup(a.rt.dataDir, []byte(body.Passphrase), w); err != nil {
		// Headers are already out, so this cannot become a clean JSON error.
		// Truncating is the honest failure: a short file fails to open rather
		// than pretending to be a backup.
		return
	}
	a.rt.noteBackupTaken()
}
