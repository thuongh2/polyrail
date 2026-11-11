package admin

import (
	"fmt"
	"net/http"

	"github.com/polyrail/polyrail/internal/catalog"
)

// POST /api/admin/catalog/sync adds the embedded catalog's missing providers (disabled, no keys)
// and missing public models; existing records are left untouched except for filling unset
// prompt-cache prices of catalog models (catalog.Sync).
func (a *API) syncCatalog(w http.ResponseWriter, r *http.Request) {
	np, nm, nc, err := catalog.Sync(r.Context(), a.S)
	if err != nil {
		fail(w, err)
		return
	}
	a.audit(r, r.PathValue("org"), "catalog.sync", "catalog", fmt.Sprintf("providers_added=%d models_added=%d cache_prices_filled=%d", np, nm, nc))
	writeJSON(w, 200, map[string]int{"providers_added": np, "models_added": nm, "cache_prices_filled": nc})
}
