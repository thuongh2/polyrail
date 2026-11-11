#!/usr/bin/env python3
"""UI smoke test: signs in, opens every page directly AND by clicking the console sidebar, and
fails on "Page not found", a doubled path or a browser console error.
  python3 scripts/ui-smoke.py http://127.0.0.1:8080 admin@local.test 'password'
Needs Python Playwright (pip install playwright && playwright install chromium)."""
import sys
from playwright.sync_api import sync_playwright

base, email, password = sys.argv[1].rstrip("/"), sys.argv[2], sys.argv[3]
public = ["/", "/models", "/status", "/docs"]
console = ["activity", "logs", "playground", "keys", "credits", "models", "providers", "members",
           "settings", "webhooks", "audit", "account", "platform/activity", "platform/orgs", "platform/providers",
           "platform/models", "platform/users", "platform/mcp", "platform/audit"]
failures, errors = [], []

def check(pg, what):
    path = pg.url[len(base):]
    if pg.get_by_text("Page not found").count() or "/app/app" in path:
        failures.append(f"{what}: landed on {path}")

with sync_playwright() as p:
    b = p.chromium.launch()
    pg = b.new_page()
    pg.on("pageerror", lambda e: errors.append(str(e)))
    for path in public:
        pg.goto(base + path); pg.wait_for_load_state("networkidle"); check(pg, "open " + path)
    pg.goto(base + "/login")
    pg.fill("input[type=email]", email); pg.fill("input[type=password]", password); pg.keyboard.press("Enter")
    pg.wait_for_url("**/app/**")
    for path in console:
        pg.goto(f"{base}/app/{path}"); pg.wait_for_load_state("networkidle"); check(pg, "open /app/" + path)
    pg.goto(base + "/app/activity"); pg.wait_for_load_state("networkidle")
    for path in console:  # click through the sidebar, as a person would
        link = pg.locator(f'nav[aria-label="Console"] a[href$="/{path}"]').first
        if not link.count():
            continue
        link.click(); pg.wait_for_timeout(300); check(pg, "click sidebar " + path)
        if not pg.url.endswith("/app/" + path):
            failures.append(f"click sidebar {path}: url is {pg.url[len(base):]}")
    pg.goto(base + "/app/webhooks"); pg.wait_for_load_state("networkidle")
    if not pg.get_by_role("heading", name="Webhooks").count():
        failures.append("webhooks: no page heading")
    pg.get_by_role("button", name="Add webhook").first.click()
    if not pg.get_by_role("dialog").get_by_text("request.failed").count():
        failures.append("webhooks: create dialog has no event choices")
    pg.keyboard.press("Escape")
    for scope in ["models", "platform/models"]:  # model name links open the public model page
        pg.goto(f"{base}/app/{scope}"); pg.wait_for_load_state("networkidle")
        link = pg.locator('main a[href*="/models/"]').first
        if link.count():
            link.click(); pg.wait_for_timeout(500); check(pg, f"click model in /app/{scope}")
            if "/app/" in pg.url:
                failures.append(f"click model in /app/{scope}: url is {pg.url[len(base):]}")
    pg.get_by_role("link", name="Models").first.click(); pg.wait_for_timeout(300); check(pg, "click header Models")
    b.close()

for f in failures + [f"page error: {e}" for e in errors]:
    print("FAIL", f)
print("ok" if not failures and not errors else f"{len(failures) + len(errors)} problem(s)")
sys.exit(1 if failures or errors else 0)
