package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestProxyPoolSealedDefaultsAndInUse(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const pw = "pool-TOPSECRET"
	p := &ProxyPool{Name: " egress ", Members: []ProxyMember{{URL: "http://a:3128", Username: "u", Password: pw, Enabled: true}, {URL: "socks5://b:1080"}}}
	if err := s.PutProxyPool(ctx, p); err != nil {
		t.Fatal(err)
	}
	if p.Name != "egress" || p.Strategy != "round-robin" || p.StickyMinutes != 30 || p.Members[0].ID == "" || p.Members[0].Password != "" {
		t.Fatalf("defaults/sealing: %+v", p)
	}
	raw, _ := s.KV.Get(ctx, "proxypool/"+p.ID)
	if strings.Contains(string(raw), pw) || !strings.Contains(string(raw), "sealed_password") {
		t.Fatalf("password not sealed at rest: %s", raw)
	}
	if r := p.Redacted(); r.Members[0].SealedPassword+r.Members[0].Password != "" || p.Members[0].SealedPassword == "" {
		t.Fatal("Redacted must strip secrets without touching the original")
	}
	// empty password keeps the stored one for the same member, URL and username
	upd := *p
	upd.Members = []ProxyMember{{ID: p.Members[0].ID, URL: "http://a:3128", Username: "u"}}
	if err := s.PutProxyPool(ctx, &upd); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetProxyPool(ctx, p.ID, true)
	if got.Members[0].Password != pw || len(got.Members) != 1 {
		t.Fatalf("kept password lost: %+v", got.Members)
	}
	upd.Members = []ProxyMember{{ID: p.Members[0].ID, URL: "http://other:3128", Username: "u"}}
	if err := s.PutProxyPool(ctx, &upd); !errors.Is(err, ErrInvalid) {
		t.Fatalf("new member URL without password: %v", err)
	}
	for _, bad := range []ProxyPool{{Name: "", Members: p.Members}, {Name: "x"}, {Name: "x", Strategy: "fastest", Members: p.Members},
		{Name: "x", StickyMinutes: 2000, Members: p.Members}, {Name: "x", Members: []ProxyMember{{ID: "1"}, {ID: "1"}}}} {
		if err := s.PutProxyPool(ctx, &bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %+v: %v", bad, err)
		}
	}
	prov := &Provider{Name: "p", Type: "openai", ProxyPool: p.ID}
	if err := s.PutProvider(ctx, prov); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProxyPool(ctx, p.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("deleted a pool in use: %v", err)
	}
	prov.ProxyPool = ""
	if err := s.PutProvider(ctx, prov); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProxyPool(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.ListProxyPools(ctx, false); len(ps) != 0 {
		t.Fatal("pool not deleted")
	}
}
