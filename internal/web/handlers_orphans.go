package web

import (
	"net/http"

	"github.com/kiineld/telemt-panel/internal/store"
)

// postRemoveOrphan removes a panel-labelled container that has no matching
// proxy row. proxy.Service.RemoveOrphan re-verifies the id against a live
// orphan list before touching Docker, so a forged or stale id here can never
// remove a container that actually belongs to a running proxy.
func (s *server) postRemoveOrphan(w http.ResponseWriter, r *http.Request, adm store.Admin) {
	if err := s.Proxy.RemoveOrphan(r.Context(), r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
