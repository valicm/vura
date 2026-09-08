const key = document.getElementById("key");
const status = document.getElementById("status");

async function refresh() {
  const s = await chrome.storage.local.get({ apiKey: "", lastStatus: null, lastAt: 0, lastError: "" });
  key.value = s.apiKey;
  if (!s.lastAt) { status.textContent = "no heartbeat sent yet"; return; }
  const ago = Math.round((Date.now() - s.lastAt) / 1000);
  status.textContent = s.lastStatus
    ? `last heartbeat ${ago}s ago → HTTP ${s.lastStatus}` + (s.lastStatus === 401 ? " (key rejected)" : "")
    : `last attempt ${ago}s ago failed: ${s.lastError || "daemon unreachable"}`;
}

document.getElementById("save").addEventListener("click", async () => {
  await chrome.storage.local.set({ apiKey: key.value.trim() });
  status.textContent = "saved";
  setTimeout(refresh, 1500);
});

refresh();
