//go:build alertsfull

package alert

import "testing"

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
