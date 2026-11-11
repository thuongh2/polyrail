package admin

import (
	"fmt"
	"net/http"

	"github.com/agent-gateway/agent-gateway/internal/catalog"
)

// POST /api/admin/catalog/sync adds the embedded catalog's missing providers (disabled, no keys)
// and missing public models; records that already exist are left untouched.
func (a *API) syncCatalog(w http.ResponseWriter, r *http.Request) {
	np, nm, err := catalog.Sync(r.Context(), a.S)
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "catalog.sync", "catalog", fmt.Sprintf("providers_added=%d models_added=%d", np, nm))
	writeJSON(w, 200, map[string]int{"providers_added": np, "models_added": nm})
}
