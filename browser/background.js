// vura browser heartbeats. Domain only, never the path or title. Posts to the
// local daemon in WakaTime's heartbeat shape so it shares the ingest path.
const ENDPOINT = "http://127.0.0.1:4242/api/v1/users/current/heartbeats.bulk";
const EVERY_MS = 60_000;          // one heartbeat per minute while a tab is focused
const DENY = [                    // never report these, even as a domain
  "chrome://", "about:", "chrome-extension://",
  "accounts.google.com", "mail.proton.me", "pass.proton.me",
  "bank", "banking", "paypal.com", "revolut.com",
];

let lastSent = { domain: "", at: 0 };

// Same key as api_key in ~/.wakatime.cfg, set on the extension's options page.
// Sent like wakatime-cli does: Basic auth with the key as the whole credential.
async function authHeader() {
  const { apiKey } = await chrome.storage.local.get({ apiKey: "" });
  return apiKey ? { Authorization: "Basic " + btoa(apiKey) } : {};
}

function domainOf(url) {
  try {
    const u = new URL(url);
    if (u.protocol !== "http:" && u.protocol !== "https:") return "";
    return u.hostname.toLowerCase();
  } catch { return ""; }
}

function denied(url, domain) {
  return DENY.some(d => url.startsWith(d) || domain.includes(d));
}

async function activeDomain() {
  const win = await chrome.windows.getLastFocused({ populate: false });
  if (!win || !win.focused) return "";
  const [tab] = await chrome.tabs.query({ active: true, windowId: win.id });
  if (!tab || !tab.url) return "";
  const domain = domainOf(tab.url);
  if (!domain || denied(tab.url, domain)) return "";
  return domain;
}

async function beat(force) {
  const domain = await activeDomain();
  if (!domain) return;
  const now = Date.now();
  if (!force && domain === lastSent.domain && now - lastSent.at < EVERY_MS - 5000) return;
  lastSent = { domain, at: now };
  const body = [{
    entity: domain, type: "domain", category: "browsing",
    time: now / 1000, project: domain, is_write: false,
    user_agent: "vura-browser/1.0 chrome/" + (navigator.userAgentData?.brands?.[0]?.version ?? "?"),
  }];
  try {
    const res = await fetch(ENDPOINT, {
      method: "POST",
      headers: { "Content-Type": "application/json", ...(await authHeader()) },
      body: JSON.stringify(body),
    });
    await chrome.storage.local.set({ lastStatus: res.status, lastAt: now });
  } catch (e) {
    // daemon down: drop it; the next minute will try again
    await chrome.storage.local.set({ lastStatus: 0, lastAt: now, lastError: String(e) });
  }
}

chrome.alarms.create("vura", { periodInMinutes: 1 });
chrome.alarms.onAlarm.addListener(() => beat(true));
chrome.tabs.onActivated.addListener(() => beat(false));
chrome.tabs.onUpdated.addListener((_id, info) => { if (info.url || info.status === "complete") beat(false); });
chrome.windows.onFocusChanged.addListener(() => beat(false));
