package alert

import (
	"net/url"
	"slices"
	"testing"

	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/discord"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/slack"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/teams"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/chat/telegram"
	"github.com/nicholas-fedor/shoutrrr/pkg/services/push/ntfy"
)

// The URLs below are exactly what the UI's builder (web/src/pages/console/Alerts.tsx buildURL)
// produces for typical inputs. Each must pass the gateway's validation for an org channel and
// parse in its shoutrrr service config to the values the user entered.
func TestUIBuiltURLs(t *testing.T) {
	mustParse := func(raw string) *url.URL {
		t.Helper()
		if err := CheckURL(raw, true, false); err != nil {
			t.Fatalf("%s: CheckURL (org channel): %v", Service(raw), err)
		}
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}

	// telegram: `telegram://${token}@telegram?chats=${chats.join(",")}`
	var tg telegram.Config
	if err := tg.SetURL(mustParse("telegram://123456789:AAH-x_Yz0123456789abcdef@telegram?chats=@mychannel,-1001234567890")); err != nil {
		t.Fatal(err)
	}
	if tg.Token != "123456789:AAH-x_Yz0123456789abcdef" || !slices.Equal(tg.Chats, []string{"@mychannel", "-1001234567890"}) {
		t.Errorf("telegram: token %q chats %q", tg.Token, tg.Chats)
	}

	// discord: from https://discord.com/api/webhooks/{id}/{token} → `discord://${token}@${id}`
	var dc discord.Config
	if err := dc.SetURL(mustParse("discord://Ab-cD_ef123@123456789012345678")); err != nil {
		t.Fatal(err)
	}
	if dc.WebhookID != "123456789012345678" || dc.Token != "Ab-cD_ef123" {
		t.Errorf("discord: id %q token %q", dc.WebhookID, dc.Token)
	}

	// slack webhook: hooks.slack.com/services/T/B/X → `slack://hook:${T}-${B}-${X}@webhook`
	var sw slack.Config
	if err := sw.SetURL(mustParse("slack://hook:T00000000-B00000000-XXXXXXXXXXXXXXXXXXXXXXXX@webhook")); err != nil {
		t.Fatal(err)
	}
	if got := sw.Token.WebhookURL(); got != "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX" || sw.Token.IsAPIToken() {
		t.Errorf("slack webhook: %q", got)
	}

	// slack bot: xoxb-… + channel ID → `slack://xoxb:${rest}@${channel}`
	var sb slack.Config
	if err := sb.SetURL(mustParse("slack://xoxb:1234567890-1234567890123-abcdefghijklmnopqrstuvwx@C0123456789")); err != nil {
		t.Fatal(err)
	}
	if !sb.Token.IsAPIToken() || sb.Channel != "C0123456789" || sb.Token.Authorization() != "Bearer xoxb-1234567890-1234567890123-abcdefghijklmnopqrstuvwx" {
		t.Errorf("slack bot: channel %q auth %q", sb.Channel, sb.Token.Authorization())
	}

	// teams: Power Automate workflow URL → `teams://?host=${encodeURIComponent(url)}`
	wf := "https://prod-00.westus.logic.azure.com:443/workflows/abc123/triggers/manual/paths/invoke?api-version=2016-06-01&sp=%2Ftriggers%2Fmanual%2Frun&sv=1.0&sig=XYZ"
	var tm teams.Config
	if err := tm.SetURL(mustParse("teams://?host=" + url.QueryEscape(wf))); err != nil {
		t.Fatal(err)
	}
	if tm.Host != wf || teams.ValidateWebhookURL(tm.Host) != nil {
		t.Errorf("teams: host %q", tm.Host)
	}
	if CheckURL("teams://?host="+url.QueryEscape("https://evil.example.com/workflows/x"), true, false) == nil {
		t.Error("teams: a non-Power Automate host passed")
	}

	// ntfy: `ntfy://${server || "ntfy.sh"}/${topic}`
	var nf ntfy.Config
	if err := nf.SetURL(mustParse("ntfy://ntfy.sh/polyrail-alerts_1")); err != nil {
		t.Fatal(err)
	}
	if nf.Host != "ntfy.sh" || nf.Topic != "polyrail-alerts_1" {
		t.Errorf("ntfy: host %q topic %q", nf.Host, nf.Topic)
	}
	if err := CheckURL("ntfy://ntfy.example.com/polyrail", false, false); err != nil {
		t.Errorf("ntfy self-hosted (platform): %v", err)
	}
}

// Org channels on vendor-hosted services must not redirect the send to another host.
func TestCheckURLTenantVendorHosts(t *testing.T) {
	for _, raw := range []string{
		"telegram://123456789:AAHabc@telegram?chats=@c&apiurl=https://evil.example.com",
		"googlechat://evil.example.com/v1/spaces/AAA/messages?key=k&token=t",
	} {
		if err := CheckURL(raw, true, false); err == nil {
			t.Errorf("%s: tenant URL with a custom host passed", Service(raw))
		}
		if err := CheckURL(raw, false, false); err != nil {
			t.Errorf("%s: platform: %v", Service(raw), err)
		}
	}
	if err := CheckURL("googlechat://chat.googleapis.com/v1/spaces/AAA/messages?key=k&token=t", true, false); err != nil {
		t.Errorf("googlechat default host: %v", err)
	}
}
