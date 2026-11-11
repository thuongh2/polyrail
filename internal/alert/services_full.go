//go:build alertsfull

package alert

import (
	"github.com/nicholas-fedor/shoutrrr/pkg/services/email/smtp"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/specialized/generic"
	"github.com/nicholas-fedor/shoutrrr/pkg/types"
)

// With -tags alertsfull: email (smtp://) and any HTTP endpoint (generic+https://). They cost
// about 16 MB of binary and 5 MB of RSS (see services in alert.go).
func init() {
	services["smtp"] = func() types.Service { return &smtp.Service{} }
	services["generic"] = func() types.Service { return &generic.Service{} }
}
