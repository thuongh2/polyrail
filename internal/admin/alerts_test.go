package admin

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/polyrail/polyrail/internal/alert"
)

func TestAlertChannelsCRUDSecretsAndTest(t *testing.T) {
	f := setup(t, Config{})
	var sent []string
	var sendErr error
	f.a.Alerts = alert.New(f.s, false, 10)
	f.a.Alerts.Send = func(_ context.Context, u string, public bool, title, body string) error {
		sent = append(sent, u)
		if public != strings.Contains(u, "telegram") {
			t.Errorf("public=%v for %s", public, u)
		}
		return sendErr
	}
	orgA := "/api/orgs/" + f.orgA.ID + "/alerts"
	tg := "telegram://123456:SECRETtoken@telegram?chats=@ops"

	// tenant: vendor-hosted services only; the URL is never returned
	r := f.do(t, "admin", "POST", orgA, `{"name":"ops","url":"`+tg+`","events":["key.quarantined","request.failed"]}`)
	expect(t, r, 201, "create")
	if strings.Contains(r.body, "SECRETtoken") || !strings.Contains(r.body, `"service":"telegram"`) {
		t.Fatalf("create response: %s", r.body)
	}
	var c struct{ ID string }
	r.json(t, &c)
	for _, bad := range []string{
		`{"name":"x","url":"generic+https://hooks.example.com/x","events":["*"]}`,
		`{"name":"x","url":"smtp://u:p@mail.example.com:587/?from=a@example.com&to=b@example.com","events":["*"]}`,
		`{"name":"x","url":"` + tg + `","events":["key.created"]}`,
		`{"name":"x","url":"` + tg + `","events":[]}`,
		`{"name":"","url":"` + tg + `","events":["*"]}`,
		`{"name":"x","url":"nope://x","events":["*"]}`,
	} {
		expect(t, f.do(t, "admin", "POST", orgA, bad), 400, bad)
	}
	r = f.do(t, "admin", "GET", orgA, "")
	expect(t, r, 200, "list")
	if strings.Contains(r.body, "SECRETtoken") || strings.Contains(r.body, "sealed") || !strings.Contains(r.body, `"events":["key.quarantined","request.failed"]`) {
		t.Fatalf("list: %s", r.body)
	}

	// test send: tenants get a generic error, operators the error text
	r = f.do(t, "admin", "POST", orgA+"/"+c.ID+"/test", "")
	expect(t, r, 200, "test")
	if r.body != "{\"ok\":true}\n" || len(sent) != 1 || sent[0] != tg {
		t.Fatalf("test: %s %v", r.body, sent)
	}
	sendErr = errors.New("telegram: chat not found at api host")
	r = f.do(t, "admin", "POST", orgA+"/"+c.ID+"/test", "")
	if !strings.Contains(r.body, `"ok":false`) || strings.Contains(r.body, "api host") {
		t.Fatalf("tenant test error: %s", r.body)
	}

	// patch: url swap re-validates; disable/enable resets failures
	expect(t, f.do(t, "admin", "PATCH", orgA+"/"+c.ID, `{"url":"gotify://gotify.example.com/AbCdEfGhIjKlMnO"}`), 400, "tenant custom host")
	r = f.do(t, "admin", "PATCH", orgA+"/"+c.ID, `{"url":"discord://tok@123456789","enabled":false,"name":"ops2"}`)
	expect(t, r, 200, "patch")
	if !strings.Contains(r.body, `"service":"discord"`) || !strings.Contains(r.body, `"enabled":false`) || strings.Contains(r.body, "tok@") {
		t.Fatalf("patch: %s", r.body)
	}
	ch, err := f.s.GetAlertChannel(context.Background(), f.orgA.ID, c.ID)
	if err != nil || ch.URL != "discord://tok@123456789" {
		t.Fatalf("stored url: %v %v", ch, err)
	}

	// isolation and roles
	expect(t, f.do(t, "outsider", "GET", orgA, ""), 404, "outsider list")
	expect(t, f.do(t, "outsider", "DELETE", "/api/orgs/"+f.orgB.ID+"/alerts/"+c.ID, ""), 404, "other org delete")
	expect(t, f.do(t, "member", "GET", orgA, ""), 403, "member")
	expect(t, f.do(t, "admin", "GET", "/api/admin/alerts", ""), 403, "platform needs operator")

	// platform: custom hosts allowed, operators see the send error
	r = f.do(t, "op", "POST", "/api/admin/alerts", `{"name":"ops","url":"gotify://10.0.0.5/AbCdEfGhIjKlMnO?disabletls=yes","events":["*"]}`)
	expect(t, r, 201, "platform generic")
	var pc struct{ ID string }
	r.json(t, &pc)
	r = f.do(t, "op", "POST", "/api/admin/alerts/"+pc.ID+"/test", "")
	if !strings.Contains(r.body, "chat not found") {
		t.Fatalf("operator test error: %s", r.body)
	}
	r = f.do(t, "op", "GET", "/api/admin/alerts", "")
	if strings.Contains(r.body, "10.0.0.5") || strings.Contains(r.body, f.orgA.ID) {
		t.Fatalf("platform list: %s", r.body)
	}
	expect(t, f.do(t, "admin", "DELETE", orgA+"/"+c.ID, ""), 204, "delete")
	expect(t, f.do(t, "admin", "DELETE", orgA+"/"+c.ID, ""), 404, "delete again")
	audit := f.do(t, "admin", "GET", "/api/orgs/"+f.orgA.ID+"/audit", "")
	if !strings.Contains(audit.body, "alert.create") || strings.Contains(audit.body, "SECRETtoken") {
		t.Fatalf("audit: %s", audit.body)
	}
}

func TestAlertTenantCustomHostWithPrivateURLsAllowed(t *testing.T) {
	f := setup(t, Config{AllowPrivateURLs: true})
	orgA := "/api/orgs/" + f.orgA.ID + "/alerts"
	expect(t, f.do(t, "admin", "POST", orgA, `{"name":"x","url":"ntfy://127.0.0.1:9/x","events":["*"]}`), 400, "private host")
	expect(t, f.do(t, "admin", "POST", orgA, `{"name":"x","url":"ntfy://ntfy.sh/topic","events":["*"]}`), 201, "ntfy default")
}
