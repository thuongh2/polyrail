//go:build alertsfull

package alert

import (
	"net/url"
	"slices"
	"testing"

	"github.com/nicholas-fedor/shoutrrr/pkg/services/email/smtp"
)

// The real shoutrrr generic service (built with -tags alertsfull) against a local receiver.
func TestSendGenericHTTP(t *testing.T) {
	sendReal(t, func(srvURL string) string { return "generic+" + srvURL + "/hook" })
	if err := CheckURL("generic+https://hooks.example.com/x", true, false); err == nil {
		t.Fatal("tenant generic channel allowed")
	}
	if err := CheckURL("smtp://u:p@mail.example.com:587/?from=a@example.com&to=b@example.com", false, false); err != nil {
		t.Fatalf("platform smtp: %v", err)
	}
}

// The SMTP URL exactly as the UI builder (Alerts.tsx buildURL) produces it.
func TestUIBuiltSMTPURL(t *testing.T) {
	raw := "smtp://ops%40example.com:p%40ss%2Fw%3Ard@smtp.example.com:587/?fromaddress=alerts%40example.com&toaddresses=ops%40example.com,oncall%40example.com"
	if err := CheckURL(raw, false, false); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	var c smtp.Config
	if err := c.SetURL(u); err != nil {
		t.Fatal(err)
	}
	if c.Username != "ops@example.com" || c.Password != "p@ss/w:rd" || c.Host != "smtp.example.com" || c.Port != 587 ||
		c.FromAddress != "alerts@example.com" || !slices.Equal(c.ToAddresses, []string{"ops@example.com", "oncall@example.com"}) {
		t.Errorf("smtp config: %+v", c)
	}
}
