#!/usr/bin/env python3
"""UI smoke test: signs in, opens every page directly AND by clicking the org sidebar, Settings sub-nav and
Admin area sidebar, tries the Ctrl+K palette, and
fails on "Page not found", a doubled path or a browser console error.
  python3 scripts/ui-smoke.py http://127.0.0.1:8080 admin@local.test 'password'
Needs Python Playwright (pip install playwright && playwright install chromium)."""
import sys
from playwright.sync_api import sync_playwright

base, email, password = sys.argv[1].rstrip("/"), sys.argv[2], sys.argv[3]
public = ["/", "/models", "/status", "/docs"]
console = ["activity", "logs", "playground", "connect", "keys", "credits", "models", "presets", "kbs", "providers", "workspaces", "members", "policies",
           "settings", "webhooks", "alerts", "observability", "sso", "audit", "account", "platform/activity", "platform/orgs", "platform/providers",
           "platform/models", "platform/users", "platform/mcp", "platform/alerts", "platform/observability", "platform/proxies", "platform/audit"]
main_nav = ["activity", "logs", "playground", "keys", "models", "presets", "kbs", "providers", "policies", "credits", "settings", "account"]
settings_nav = ["settings", "members", "workspaces", "sso", "webhooks", "alerts", "observability", "audit"]
admin_nav = ["platform/activity", "platform/orgs", "platform/users", "platform/providers", "platform/models", "platform/proxies", "platform/mcp",
             "platform/alerts", "platform/observability", "platform/audit"]
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
    pg.fill("input[type=email]", email); pg.keyboard.press("Enter")  # email first: the domain picks SSO or password
    pg.fill("input[type=password]", password); pg.keyboard.press("Enter")
    pg.wait_for_url("**/app/**")
    for path in console:
        pg.goto(f"{base}/app/{path}"); pg.wait_for_load_state("networkidle"); check(pg, "open /app/" + path)
    # Click through each navigation, as a person would: the org sidebar, the Settings hub sub-nav and the
    # Admin area sidebar (which replaces the org sidebar under /app/platform).
    navs = [("/app/activity", "Console", main_nav), ("/app/settings", "Settings", settings_nav), ("/app/platform/activity", "Admin area", admin_nav)]
    for start, label, paths in navs:
        pg.goto(base + start); pg.wait_for_load_state("networkidle")
        for path in paths:
            link = pg.locator(f'nav[aria-label="{label}"] a[href$="/{path}"]').first
            if not link.count():
                failures.append(f"{label} nav: no link to {path}")
                continue
            link.click(); pg.wait_for_timeout(300); check(pg, f"click {label} nav {path}")
            if not pg.url.split("?")[0].endswith("/app/" + path):
                failures.append(f"click {label} nav {path}: url is {pg.url[len(base):]}")
    pg.goto(base + "/app/alerts"); pg.wait_for_load_state("networkidle")
    if pg.locator('nav[aria-label="Console"] a[aria-current="page"]').inner_text() != "Settings":
        failures.append("settings page /app/alerts does not highlight Settings in the sidebar")
    if pg.locator('nav[aria-label="Breadcrumb"]').inner_text().replace("\n", " ") != "Settings / Integrations / Alerts":
        failures.append(f"alerts breadcrumb: {pg.locator('nav[aria-label=Breadcrumb]').inner_text()!r}")
    pg.goto(base + "/app/platform/proxies"); pg.wait_for_load_state("networkidle")
    if pg.locator('nav[aria-label="Console"]').count() or not pg.get_by_text("Admin", exact=True).count():
        failures.append("admin area: org sidebar still shown or no Admin badge")
    # Command palette: Ctrl+K, type, Enter.
    pg.goto(base + "/app/activity"); pg.wait_for_load_state("networkidle")
    pg.keyboard.press("Control+k")
    box = pg.get_by_role("combobox", name="Search pages and actions")
    box.wait_for(); box.fill("proxies"); pg.keyboard.press("Enter"); pg.wait_for_timeout(500)
    if not pg.url.split("?")[0].endswith("/app/platform/proxies"):
        failures.append(f"palette: proxies went to {pg.url[len(base):]}")
    pg.keyboard.press("Control+k"); pg.get_by_role("combobox", name="Search pages and actions").fill("create api key")
    pg.keyboard.press("Enter"); pg.wait_for_timeout(800)
    if not pg.get_by_role("dialog").get_by_text("Create API key").count():
        failures.append("palette: Create API key did not open the create dialog")
    pg.keyboard.press("Escape")
    pg.goto(base + "/app/webhooks"); pg.wait_for_load_state("networkidle")
    if not pg.get_by_role("heading", name="Webhooks").count():
        failures.append("webhooks: no page heading")
    pg.get_by_role("button", name="Add webhook").first.click()
    if not pg.get_by_role("dialog").get_by_text("request.failed").count():
        failures.append("webhooks: create dialog has no event choices")
    pg.keyboard.press("Escape")
    pg.goto(base + "/app/platform/providers"); pg.wait_for_load_state("networkidle")
    row = pg.locator('main a[href*="/platform/providers/"]').first  # the first provider row's name link
    if row.count():
        row.click(); pg.wait_for_timeout(500); check(pg, "open provider detail")
        if "/app/platform/providers/" not in pg.url:
            failures.append(f"open provider detail: url is {pg.url[len(base):]}")
        elif not pg.get_by_role("button", name="Edit provider").count():
            failures.append("provider detail: no Edit provider button")
    else:
        failures.append("platform/providers: no provider row to open")
    for scope in ["models", "platform/models"]:  # model name links open the public model page
        pg.goto(f"{base}/app/{scope}"); pg.wait_for_load_state("networkidle")
        link = pg.locator('main a[href*="/models/"]').first
        if link.count():
            link.click(); pg.wait_for_timeout(500); check(pg, f"click model in /app/{scope}")
            if "/app/" in pg.url:
                failures.append(f"click model in /app/{scope}: url is {pg.url[len(base):]}")
    # Switch workspace once (the top-bar switcher) and revisit a workspace-scoped page in it.
    pg.goto(base + "/app/keys"); pg.wait_for_load_state("networkidle")
    switcher = pg.get_by_role("combobox", name="Workspace")
    if not switcher.count():
        failures.append("workspace switcher missing from the top bar")
    else:
        current = switcher.inner_text()  # read before opening: an open menu hides the rest of the page from roles
        switcher.click()
        options = pg.get_by_role("option")
        target = next((options.nth(i) for i in range(options.count()) if options.nth(i).inner_text() not in (current, "All workspaces")), None)
        if target is None:
            target = pg.get_by_role("option", name="All workspaces")
        name = target.inner_text(); target.click(); pg.wait_for_load_state("networkidle")
        pg.goto(base + "/app/policies"); pg.wait_for_load_state("networkidle"); check(pg, "policies after switching workspace")
        pg.locator('nav[aria-label="Console"] a[href$="/keys"]').first.click(); pg.wait_for_timeout(500); check(pg, "keys after switching workspace")
        if name not in switcher.inner_text():
            failures.append(f"workspace switch to {name} did not stick (shows {switcher.inner_text()})")
        if name != "Default" and "ws=" not in pg.url:
            failures.append(f"workspace switch: no ?ws= in {pg.url[len(base):]}")
    pg.get_by_role("link", name="Models").first.click(); pg.wait_for_timeout(300); check(pg, "click header Models")
    b.close()

for f in failures + [f"page error: {e}" for e in errors]:
    print("FAIL", f)
print("ok" if not failures and not errors else f"{len(failures) + len(errors)} problem(s)")
sys.exit(1 if failures or errors else 0)
