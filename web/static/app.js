// Thanos Web UI — frontend logic

const API = "/api";

// Auth state
let authToken = localStorage.getItem("thanos_auth");
let authUser = localStorage.getItem("thanos_user");

// Build Basic Auth header value from token.
function authHeaders() {
  if (!authToken) return {};
  return { Authorization: "Basic " + authToken };
}

// ── Login / Logout ──

function showLogin() {
  document.getElementById("loginOverlay").style.display = "flex";
  document.getElementById("dashboard").classList.add("dashboard-hidden");
}

function showDashboard() {
  document.getElementById("loginOverlay").style.display = "none";
  document.getElementById("dashboard").classList.remove("dashboard-hidden");
  loadHealth();
  loadContainers();
}

document.getElementById("loginBtn").onclick = async () => {
  const user = document.getElementById("loginUser").value;
  const pass = document.getElementById("loginPass").value;
  const errEl = document.getElementById("loginError");
  errEl.textContent = "";

  if (!user || !pass) {
    errEl.textContent = "Please enter username and password.";
    return;
  }

  const token = btoa(user + ":" + pass);

  // Test auth by hitting the containers endpoint.
  try {
    const r = await fetch(`${API}/containers`, {
      headers: { Authorization: "Basic " + token },
    });
    if (r.status === 401) {
      errEl.textContent = "Invalid username or password.";
      return;
    }
    if (!r.ok) {
      errEl.textContent = "Server error: " + r.status;
      return;
    }

    // Success — store credentials.
    authToken = token;
    authUser = user;
    localStorage.setItem("thanos_auth", token);
    localStorage.setItem("thanos_user", user);
    showDashboard();
  } catch (e) {
    errEl.textContent = "Connection error: " + e.message;
  }
};

document.getElementById("logoutBtn").onclick = () => {
  localStorage.removeItem("thanos_auth");
  localStorage.removeItem("thanos_user");
  authToken = null;
  authUser = null;
  showLogin();
};

// Allow Enter key to submit login.
document.getElementById("loginPass").addEventListener("keydown", (e) => {
  if (e.key === "Enter") document.getElementById("loginBtn").click();
});
document.getElementById("loginUser").addEventListener("keydown", (e) => {
  if (e.key === "Enter") document.getElementById("loginPass").focus();
});

// ── API calls ──

async function fetchJSON(url, opts = {}) {
  const headers = { ...authHeaders(), ...(opts.headers || {}) };
  const r = await fetch(url, { ...opts, headers });
  if (r.status === 401) {
    // Token expired or invalid — go back to login.
    localStorage.removeItem("thanos_auth");
    authToken = null;
    showLogin();
    throw new Error("Authentication required");
  }
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return r.json();
}

// ── Health ──

async function loadHealth() {
  try {
    const h = await fetchJSON(`${API}/health`);
    document.getElementById("healthStatus").textContent =
      `✅ Thanos ${h.version || ""} · Docker: ${h.docker || "unknown"}`;
  } catch {
    document.getElementById("healthStatus").textContent =
      `⚠️ Thanos unreachable`;
  }
}

// ── Containers ──

// Container cache for stable ordering and preserving last-known-good stats.
// Keyed by container ID. Persists across refresh cycles.
const containerCache = new Map(); // id → { cpu, mem }
let refreshInFlight = false;

// Sort containers by display name (case-insensitive) for a stable order.
function sortContainers(containers) {
  return containers.slice().sort((a, b) => {
    const an = (a.display_name || a.name || "").toLowerCase();
    const bn = (b.display_name || b.name || "").toLowerCase();
    if (an !== bn) return an < bn ? -1 : 1;
    return 0;
  });
}

async function loadContainers() {
  if (refreshInFlight) return; // prevent overlapping refreshes
  refreshInFlight = true;
  const section = document.getElementById("containers");
  try {
    const data = await fetchJSON(`${API}/containers`);
    let containers = data.containers || [];
    containers = sortContainers(containers);

    if (containers.length === 0) {
      section.innerHTML =
        '<div class="loading">No Thanos-managed containers. Add <code>thanos.enabled=true</code> label to a container.</div>';
      containerCache.clear();
      refreshInFlight = false;
      return;
    }

    // Smart DOM update: only rebuild cards that are new or changed.
    // This prevents the hamburger menu from closing and stats from flickering.

    // Remove any loading placeholder from the initial render.
    const loadingEl = section.querySelector(".loading");
    if (loadingEl) {
      loadingEl.remove();
    }

    const seenIds = new Set(containers.map((c) => c.id));
    const existingCards = section.querySelectorAll(".container-card");

    // Remove cards for containers that no longer exist.
    existingCards.forEach((card) => {
      if (!seenIds.has(card.dataset.id)) {
        containerCache.delete(card.dataset.id);
        card.remove();
      }
    });

    // Update or create cards in sorted order.
    let insertBefore = section.firstChild;
    for (const c of containers) {
      let card = section.querySelector(`.container-card[data-id="${c.id}"]`);
      if (card) {
        // Card exists — update its content in place.
        updateCardInPlace(card, c);
        // Move it to the correct position if needed.
        if (card !== insertBefore) {
          section.insertBefore(card, insertBefore);
        } else {
          insertBefore = card.nextSibling;
        }
      } else {
        // New card — create it.
        const wrapper = document.createElement("div");
        wrapper.innerHTML = renderCard(c);
        card = wrapper.firstElementChild;
        section.insertBefore(card, insertBefore);
      }
    }

    // Query stats for running containers via REST (one-shot).
    fetchStatsForCards(containers);
  } catch (e) {
    if (!section.querySelector(".container-card")) {
      section.innerHTML = `<div class="loading">Failed to load containers: ${e.message}</div>`;
    }
  }
  refreshInFlight = false;
}

// updateCardInPlace updates an existing card element with new data without
// rebuilding the entire HTML. This preserves the hamburger menu open state
// and prevents CPU/MEM flicker.
function updateCardInPlace(card, c) {
  // Update state badge.
  const badge = card.querySelector(".state-badge");
  if (badge) {
    badge.className = `state-badge state-${c.state}`;
    badge.textContent = c.state;
  }

  // Update name and last-online badge.
  const nameEl = card.querySelector(".name");
  if (nameEl) {
    const isDormant = c.state === "dormant" || c.state === "crashed";
    const lastOnlineEl = nameEl.querySelector(".last-online");
    // Remove existing last-online if container is no longer dormant.
    if (!isDormant && lastOnlineEl) {
      lastOnlineEl.remove();
    }
    // Update or insert last-online for dormant/crashed containers.
    if (isDormant && c.last_started) {
      const html = `<span class="last-online" title="Last online ${new Date(c.last_started).toLocaleString()}">Last online ${timeAgo(new Date(c.last_started))}</span>`;
      if (lastOnlineEl) {
        lastOnlineEl.outerHTML = html;
      } else {
        nameEl.insertAdjacentHTML("beforeend", html);
      }
    }
    // Update the name text (first child node).
    nameEl.firstChild.textContent = c.display_name || c.name;
  }

  // Update stats line.
  const statsEl = card.querySelector(".card-stats");
  if (statsEl) {
    const isDormant = c.state === "dormant" || c.state === "crashed";
    let trafficInfo = "No traffic yet";
    if (!isDormant && c.last_traffic) {
      trafficInfo = timeAgo(new Date(c.last_traffic));
    }
    let startedInfo = "—";
    if (!isDormant && c.last_started) {
      startedInfo = timeAgo(new Date(c.last_started));
    }
    statsEl.innerHTML = `
      <span title="Last started">Started: ${startedInfo}</span>
      <span title="Ports">Ports: ${c.ports ? c.ports.join(", ") : "—"}</span>
      <span title="Snap timeout">Snap: ${c.snap_timeout ? (c.snap_timeout / 3600).toFixed(c.snap_timeout % 3600 === 0 ? 0 : 1) : 0}h</span>
      <span title="Last traffic">Traffic: ${trafficInfo}</span>
    `;
  }

  // Update play/stop toggle button.
  const canStart = c.state === "dormant" || c.state === "crashed";
  const isTransient = c.state === "starting" || c.state === "stopping";
  const toggleBtn = card.querySelector(".playstop-btn");
  if (toggleBtn) {
    if (canStart) {
      toggleBtn.classList.remove("stop");
      toggleBtn.classList.add("play");
      toggleBtn.title = "Start Server";
      toggleBtn.innerHTML = `<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.5v11l9-5.5z"/></svg>`;
    } else {
      toggleBtn.classList.remove("play");
      toggleBtn.classList.add("stop");
      toggleBtn.title = "Stop Server";
      toggleBtn.innerHTML = `<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><rect x="3" y="3" width="10" height="10" rx="1.5"/></svg>`;
    }
    toggleBtn.disabled = isTransient;
  }
}

// ── Stats Query ──

async function fetchStatsForCards(containers) {
  for (const c of containers) {
    if (c.state !== "running") {
      // Keep last known good value if the container stopped recently.
      const cached = containerCache.get(c.id);
      if (cached) {
        const cardEl = document.querySelector(
          `.container-card[data-id="${c.id}"]`,
        );
        if (cardEl) {
          const cpuEl = cardEl.querySelector(".card-cpu");
          const memEl = cardEl.querySelector(".card-mem");
          if (cpuEl) cpuEl.textContent = `CPU: ${cached.cpu}`;
          if (memEl) memEl.textContent = `MEM: ${cached.mem}`;
        }
      }
      continue;
    }
    try {
      const stats = await fetchJSON(`${API}/containers/${c.id}/stats`);
      const cardEl = document.querySelector(
        `.container-card[data-id="${c.id}"]`,
      );
      if (!cardEl) continue;
      const cpuEl = cardEl.querySelector(".card-cpu");
      const memEl = cardEl.querySelector(".card-mem");
      // Only update if we got valid values (non-empty).
      if ((stats.cpu && stats.cpu !== "0.0%") || stats.mem) {
        if (cpuEl) cpuEl.textContent = `CPU: ${stats.cpu}`;
        if (memEl) memEl.textContent = `MEM: ${stats.mem}`;
        // Cache last known good values.
        containerCache.set(c.id, { cpu: stats.cpu, mem: stats.mem });
      }
    } catch (e) {
      // Stats query failed — keep last known good value from cache.
      const cached = containerCache.get(c.id);
      if (cached) {
        const cardEl = document.querySelector(
          `.container-card[data-id="${c.id}"]`,
        );
        if (cardEl) {
          const cpuEl = cardEl.querySelector(".card-cpu");
          const memEl = cardEl.querySelector(".card-mem");
          if (cpuEl) cpuEl.textContent = `CPU: ${cached.cpu}`;
          if (memEl) memEl.textContent = `MEM: ${cached.mem}`;
        }
      }
    }
  }
}

function renderCard(c) {
  const canStart = c.state === "dormant" || c.state === "crashed";
  const isTransient = c.state === "starting" || c.state === "stopping";
  const isDormant = canStart;
  let trafficInfo = "No traffic yet";
  if (!isDormant && c.last_traffic) {
    const ago = timeAgo(new Date(c.last_traffic));
    trafficInfo = ago;
  }
  let startedInfo = "—";
  if (!isDormant && c.last_started) {
    startedInfo = timeAgo(new Date(c.last_started));
  }
  const cached = containerCache.get(c.id);
  const lastOnlineHtml =
    isDormant && c.last_started
      ? `<span class="last-online" title="Last online ${new Date(c.last_started).toLocaleString()}">Last online ${timeAgo(new Date(c.last_started))}</span>`
      : "";
  return `
    <div class="container-card" data-id="${c.id}">
      <div class="card-main">
        <div class="card-left">
          <span class="state-badge state-${c.state}">${c.state}</span>
          <div class="card-info">
            <div class="name">${escapeHTML(c.display_name || c.name)}${lastOnlineHtml}</div>
            <div class="card-usage">
              <span class="card-cpu" title="CPU usage">${cached ? `CPU: ${cached.cpu}` : "CPU: —"}</span>
              <span class="card-mem" title="Memory usage">${cached ? `MEM: ${cached.mem}` : "MEM: —"}</span>
            </div>
            <div class="card-stats">
              <span title="Last started">Started: ${startedInfo}</span>
              <span title="Ports">Ports: ${c.ports ? c.ports.join(", ") : "—"}</span>
              <span title="Snap timeout">Snap: ${c.snap_timeout ? (c.snap_timeout / 3600).toFixed(c.snap_timeout % 3600 === 0 ? 0 : 1) : 0}h</span>
              <span title="Last traffic">Traffic: ${trafficInfo}</span>
            </div>
          </div>
        </div>
        <div class="card-actions">
          <button class="playstop-btn ${canStart ? "play" : "stop"}" data-action="toggle" title="${canStart ? "Start Server" : "Stop Server"}" ${isTransient ? "disabled" : ""}>
            ${
              canStart
                ? `<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.5v11l9-5.5z"/></svg>`
                : `<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><rect x="3" y="3" width="10" height="10" rx="1.5"/></svg>`
            }
          </button>
          <div class="burger-menu">
            <button class="burger-btn" data-action="burger" title="Menu">
              <svg width="18" height="18" viewBox="0 0 18 18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">
                <path d="M3 5h12M3 9h12M3 13h12"/>
              </svg>
            </button>
            <div class="burger-dropdown">
              <button class="dropdown-item" data-action="logs" data-id="${c.id}">View Docker Logs</button>
              <button class="dropdown-item" data-action="state-logs" data-id="${c.id}">View State Logs</button>
              <button class="dropdown-item" data-action="traffic" data-id="${c.id}">View Traffic</button>
              <button class="dropdown-item" data-action="edit" data-id="${c.id}">Edit Settings</button>
              <button class="dropdown-item dropdown-danger" data-action="remove" data-id="${c.id}">Remove from Thanos</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  `;
}

function timeAgo(date) {
  const seconds = Math.floor((Date.now() - date.getTime()) / 1000);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ${minutes % 60}m ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

// cardName extracts just the server name from a container card. The .name
// element also holds a .last-online span for dormant/crashed containers, so
// `.name.textContent` would concatenate them with no separator (issue #7).
// The first child node is always the bare text node set by renderCard.
function cardName(card) {
  const nameEl = card.querySelector(".name");
  if (!nameEl) return "";
  const first = nameEl.firstChild;
  return first && first.nodeType === Node.TEXT_NODE
    ? first.textContent.trim()
    : nameEl.textContent.trim();
}

let actionsBound = false;

function bindActions() {
  // Only bind the global click listener once to prevent duplicate handlers.
  if (actionsBound) return;
  actionsBound = true;

  // Handle clicks on card action buttons (including burger dropdown items).
  document.addEventListener("click", async (e) => {
    // Process play/stop toggle clicks first (they're outside the burger menu).
    const toggleBtn = e.target.closest(".playstop-btn[data-action='toggle']");
    if (toggleBtn) {
      const card = toggleBtn.closest(".container-card");
      if (!card) return;
      const id = card.dataset.id;
      const isPlay = toggleBtn.classList.contains("play");
      const endpoint = isPlay ? "start" : "stop";
      toggleBtn.disabled = true;
      try {
        await fetchJSON(`${API}/containers/${id}/${endpoint}`, {
          method: "POST",
        });
      } catch (e) {
        console.error(e);
      }
      await loadContainers();
      return;
    }

    // Close all burger dropdowns when clicking outside the burger menu.
    if (!e.target.closest(".burger-menu")) {
      document
        .querySelectorAll(".burger-dropdown.active")
        .forEach((d) => d.classList.remove("active"));
      return; // Don't process other actions when clicking outside.
    }

    const btn = e.target.closest("[data-action]");
    if (!btn) return;
    const card = btn.closest(".container-card");
    if (!card) return;
    const id = card.dataset.id;
    const action = btn.dataset.action;

    if (action === "burger") {
      // Toggle the dropdown. Use closest to reliably find the dropdown
      // even if the SVG was the click target.
      e.stopPropagation();
      const menu = btn.closest(".burger-menu");
      const dropdown = menu.querySelector(".burger-dropdown");
      const wasActive = dropdown.classList.contains("active");
      // Close all other dropdowns first.
      document.querySelectorAll(".burger-dropdown.active").forEach((d) => {
        if (d !== dropdown) d.classList.remove("active");
      });
      // Toggle this dropdown. If it was already active, close it.
      // If it was not active, open it.
      if (wasActive) {
        dropdown.classList.remove("active");
      } else {
        dropdown.classList.add("active");
      }
      return;
    }

    // Close any open dropdowns.
    document
      .querySelectorAll(".burger-dropdown.active")
      .forEach((d) => d.classList.remove("active"));

    if (action === "remove") {
      await removeFromDashboard(id, card);
      return;
    }
    if (action === "logs") {
      openLogViewer(id, card);
      return;
    }
    if (action === "state-logs") {
      openStateLogViewer(id, card);
      return;
    }
    if (action === "traffic") {
      openTrafficViewer(id, card);
      return;
    }
    if (action === "edit") {
      openCardEditor(id);
      return;
    }
    if (action === "start" || action === "stop") {
      btn.disabled = true;
      try {
        await fetchJSON(`${API}/containers/${id}/${action}`, {
          method: "POST",
        });
      } catch (e) {
        console.error(e);
      }
      await loadContainers();
    }
  });
}

async function openCardEditor(id) {
  // Fetch all containers to find this one's current labels.
  const data = await fetchJSON(`${API}/all-containers`);
  const container = (data.containers || []).find((c) => c.id === id);
  if (!container) {
    alert("Container not found.");
    return;
  }
  // Reuse the label editor from the manage modal.
  openModal("Edit Settings", '<div class="loading">Loading...</div>');
  showLabelEditorInModal(container);
}

function showLabelEditorInModal(container) {
  modalBody.innerHTML = `
    <div class="label-edit">
      <div class="label-edit-header">Configuring: ${escapeHTML(container.name)}</div>
      <div class="label-edit-row">
        <label>Display Name</label>
        <input id="le_display_name" value="${escapeHTML(container.display_name || "")}" placeholder="Friendly name">
      </div>
      <div class="label-edit-row">
        <label>Snap Timeout (hours)</label>
        <input id="le_snap_timeout" type="number" step="0.25" value="${container.snap_timeout ? (container.snap_timeout / 3600).toFixed(2) : "0.25"}" placeholder="0.25">
      </div>
      <div class="label-edit-checkbox-row">
        <input id="le_crash_detection" type="checkbox" checked>
        <label for="le_crash_detection">Crash Detection</label>
      </div>
      <div class="label-edit-checkbox-row">
        <input id="le_delete_original" type="checkbox" checked>
        <label for="le_delete_original">Delete original container after recreation</label>
      </div>
      <div class="label-edit-actions">
        <button class="btn-save-labels" data-action="save" data-id="${container.id}">Save</button>
        <button class="btn-cancel-labels" data-action="cancel">Cancel</button>
      </div>
      <p class="warning">Container will be recreated with new labels. It must be stopped first if running.</p>
    </div>
  `;

  modalBody.querySelector('[data-action="save"]').onclick = async () => {
    const labels = {
      "thanos.enabled": "true",
      "thanos.display_name": modalBody.querySelector("#le_display_name").value,
      "thanos.snap_timeout": modalBody.querySelector("#le_snap_timeout").value,
      "thanos.crash_detection": modalBody.querySelector("#le_crash_detection")
        .checked
        ? "true"
        : "false",
    };
    const deleteOriginal = modalBody.querySelector(
      "#le_delete_original",
    ).checked;
    try {
      modalBody.querySelector(".warning").textContent =
        "Saving... (recreating container)";
      await fetchJSON(`${API}/labels`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          container_id: container.id,
          labels,
          delete_original: deleteOriginal,
        }),
      });
      closeModal();
      loadContainers();
    } catch (e) {
      modalBody.querySelector(".warning").textContent = "Error: " + e.message;
    }
  };
  modalBody.querySelector('[data-action="cancel"]').onclick = () =>
    closeModal();
}

async function removeFromDashboard(id, card) {
  const name = cardName(card);
  if (
    !confirm(
      `Remove "${name}" from Thanos management? The container will be recreated without Thanos labels.`,
    )
  )
    return;
  const labels = {
    "thanos.enabled": "",
    "thanos.display_name": "",
    "thanos.snap_timeout": "",
    "thanos.crash_detection": "",
    "thanos.notify_discord": "",
    "thanos.keep_running_on_boot": "",
  };
  try {
    await fetchJSON(`${API}/labels`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ container_id: id, labels, delete_original: true }),
    });
    // Clean up cache and remove the card immediately to prevent 500 errors
    // from stats queries on the now-removed container.
    containerCache.delete(id);
    card.remove();
    await loadContainers();
  } catch (e) {
    alert("Failed to remove: " + e.message);
  }
}

function escapeHTML(s) {
  const d = document.createElement("div");
  d.textContent = s;
  return d.innerHTML;
}

document.getElementById("refreshBtn").onclick = loadContainers;

// ── Label Management Modal ──

const modal = document.getElementById("labelModal");
const modalBody = document.getElementById("modalBody");
const modalTitle = document.getElementById("modalTitle");
let allContainers = [];
let cachedInterfaces = [];

function openModal(title, html) {
  modalTitle.textContent = title;
  modalBody.innerHTML = html;
  modal.classList.add("active");
}

function closeModal() {
  modal.classList.remove("active");
}

document.getElementById("settingsBtn").onclick = async () => {
  await showSettingsModal();
};

document.getElementById("addContainerBtn").onclick = async () => {
  openModal(
    "Manage Thanos Labels",
    '<div class="loading">Loading containers...</div>',
  );
  await loadAllContainers();
};

document.getElementById("modalClose").onclick = () => {
  closeModal();
};

modal.addEventListener("click", (e) => {
  if (e.target === modal) closeModal();
});

async function showSettingsModal() {
  openModal("Settings", '<div class="loading">Loading settings...</div>');
  try {
    const data = await fetchJSON(`${API}/settings`);
    cachedInterfaces = data.interfaces || [];
    renderSettingsModal(data);
  } catch (e) {
    modalBody.innerHTML = `<div class="loading">Failed to load settings: ${e.message}</div>`;
  }
}

function renderSettingsModal(data) {
  const options = cachedInterfaces
    .map((iface) => {
      const addrs = (iface.addrs || []).join(", ");
      const selected = iface.name === data.network_interface ? "selected" : "";
      return `<option value="${escapeHTML(iface.name)}" ${selected}>${escapeHTML(iface.name)} (${escapeHTML(addrs)})</option>`;
    })
    .join("");

  modalBody.innerHTML = `
    <div class="modal-form">
      <div>
        <label for="settings_username">Admin Username</label>
        <input id="settings_username" value="${escapeHTML(data.username || "")}" autocomplete="username">
      </div>
      <div>
        <label for="settings_password">New Password</label>
        <input id="settings_password" type="password" placeholder="Leave blank to keep current password" autocomplete="new-password">
      </div>
      <div>
        <label for="settings_password2">Confirm New Password</label>
        <input id="settings_password2" type="password" placeholder="Repeat the new password" autocomplete="new-password">
      </div>
      <div>
        <label for="settings_iface">Target Adapter</label>
        <select id="settings_iface">
          ${options}
        </select>
      </div>
      <p class="modal-note modal-warning">
        Use <strong>Loopback</strong> to test <strong>127.0.0.1</strong> connections on Windows. Use your LAN adapter for connections from other machines.
      </p>

      <div class="section-title">Discord</div>
      <div>
        <label for="settings_guild_id">Guild ID</label>
        <input id="settings_guild_id" value="${escapeHTML(data.discord_guild_id || "")}" placeholder="Discord guild ID">
      </div>
      <div>
        <label for="settings_status_channel">Status Channel ID</label>
        <input id="settings_status_channel" value="${escapeHTML(data.discord_channel_id || "")}" placeholder="Channel for status embed">
      </div>
      <div>
        <label for="settings_log_channel">Log Channel ID</label>
        <input id="settings_log_channel" value="${escapeHTML(data.discord_log_channel_id || "")}" placeholder="Channel for event notifications">
      </div>

      <div class="section-title">IP Blacklist</div>
      <div>
        <label for="settings_blacklist">Ignored IP Patterns (one per line)</label>
        <textarea id="settings_blacklist" rows="5" placeholder="23.111.14.183/32&#10;10.0.0.0/8&#10;# Lines starting with # are ignored">${escapeHTML(data.blacklist || "")}</textarea>
        <p class="modal-note">Packets from these IPs/subnets will be silently dropped. Supports CIDR notation and bare IPs. One entry per line.</p>
      </div>

      <div class="modal-actions">
        <button class="btn btn-save-settings" id="settingsSaveBtn">Save Settings</button>
        <button class="btn btn-cancel-settings" id="settingsCancelBtn">Cancel</button>
      </div>
      <p id="settingsMsg" class="modal-note"></p>
    </div>
  `;

  modalBody.querySelector("#settingsCancelBtn").onclick = () => closeModal();
  modalBody.querySelector("#settingsSaveBtn").onclick = saveSettings;
}

async function saveSettings() {
  const msg = modalBody.querySelector("#settingsMsg");
  const username = modalBody.querySelector("#settings_username").value.trim();
  const password = modalBody.querySelector("#settings_password").value;
  const password2 = modalBody.querySelector("#settings_password2").value;
  const networkInterface = modalBody.querySelector("#settings_iface").value;

  msg.textContent = "";

  if (!username) {
    msg.textContent = "Admin username is required.";
    return;
  }
  if (password || password2) {
    if (password !== password2) {
      msg.textContent = "New passwords do not match.";
      return;
    }
  }

  const body = {
    username,
    password,
    confirm_password: password2,
    network_interface: networkInterface,
    discord_guild_id: modalBody
      .querySelector("#settings_guild_id")
      .value.trim(),
    discord_channel_id: modalBody
      .querySelector("#settings_status_channel")
      .value.trim(),
    discord_log_channel_id: modalBody
      .querySelector("#settings_log_channel")
      .value.trim(),
    blacklist: modalBody.querySelector("#settings_blacklist").value,
  };

  const saveBtn = modalBody.querySelector("#settingsSaveBtn");
  saveBtn.disabled = true;
  saveBtn.textContent = "Saving...";
  try {
    await fetchJSON(`${API}/settings`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });

    if (password) {
      authToken = btoa(username + ":" + password);
      authUser = username;
      localStorage.setItem("thanos_auth", authToken);
      localStorage.setItem("thanos_user", authUser);
    } else {
      authUser = username;
      localStorage.setItem("thanos_user", authUser);
    }

    closeModal();
    await loadHealth();
    await loadContainers();
  } catch (e) {
    msg.textContent = "Error: " + e.message;
  } finally {
    saveBtn.disabled = false;
    saveBtn.textContent = "Save Settings";
  }
}

async function loadAllContainers() {
  try {
    const data = await fetchJSON(`${API}/all-containers`);
    allContainers = data.containers || [];
    renderAllContainers();
  } catch (e) {
    modalBody.innerHTML = `<div class="loading">Failed to load: ${e.message}</div>`;
  }
}

function renderAllContainers() {
  if (allContainers.length === 0) {
    modalBody.innerHTML =
      '<div class="loading">No Docker containers found.</div>';
    return;
  }

  modalBody.innerHTML = allContainers
    .map((c) => {
      const enabled = c.thanos_enabled;
      return `
      <div class="modal-container-row" data-id="${c.id}">
        <div class="info">
          <div class="name">${escapeHTML(c.name)}</div>
          <div class="meta">${escapeHTML(c.image || "")} · ${c.state}</div>
        </div>
        <span class="state-badge ${enabled ? "state-running" : "state-dormant"}">${enabled ? "Thanos" : "Unmanaged"}</span>
        <button class="settings-btn" data-action="edit" data-id="${c.id}">${enabled ? "Edit" : "Enable"}</button>
        ${enabled ? `<button class="settings-btn" data-action="disable" data-id="${c.id}">Remove</button>` : ""}
      </div>
    `;
    })
    .join("");

  // Bind edit/disable buttons.
  modalBody.querySelectorAll("[data-action]").forEach((btn) => {
    btn.onclick = () => {
      const id = btn.dataset.id;
      const action = btn.dataset.action;
      const container = allContainers.find((c) => c.id === id);
      if (action === "edit") {
        showLabelEditor(container);
      } else if (action === "disable") {
        removeThanosLabels(container);
      }
    };
  });
}

function showLabelEditor(container) {
  const row = modalBody.querySelector(`[data-id="${container.id}"]`);
  row.innerHTML = `
    <div class="label-edit">
      <div class="label-edit-header">Configuring: ${escapeHTML(container.name)}</div>
      <div class="label-edit-row">
        <label>Display Name</label>
        <input id="le_display_name" value="${escapeHTML(container.display_name || "")}" placeholder="Friendly name">
      </div>
      <div class="label-edit-row">
        <label>Snap Timeout (hours)</label>
        <input id="le_snap_timeout" type="number" step="0.25" value="${container.snap_timeout ? (container.snap_timeout / 3600).toFixed(2) : "0.25"}" placeholder="0.25">
      </div>
      <div class="label-edit-checkbox-row">
        <input id="le_crash_detection" type="checkbox" checked>
        <label for="le_crash_detection">Crash Detection</label>
      </div>
      <div class="label-edit-checkbox-row">
        <input id="le_delete_original" type="checkbox">
        <label for="le_delete_original">Delete original container after recreation</label>
      </div>
      <div class="label-edit-actions">
        <button class="btn-save-labels" data-action="save" data-id="${container.id}">Save</button>
        <button class="btn-cancel-labels" data-action="cancel">Cancel</button>
      </div>
      <p class="warning">⚠ Container will be recreated with new labels. It must be stopped first if running.</p>
    </div>
  `;

  row.querySelector('[data-action="save"]').onclick = async () => {
    const labels = {
      "thanos.enabled": "true",
      "thanos.display_name": row.querySelector("#le_display_name").value,
      "thanos.snap_timeout": row.querySelector("#le_snap_timeout").value,
      "thanos.crash_detection": row.querySelector("#le_crash_detection").checked
        ? "true"
        : "false",
    };
    const deleteOriginal = row.querySelector("#le_delete_original").checked;
    try {
      row.querySelector(".warning").textContent =
        "Saving... (recreating container)";
      await fetchJSON(`${API}/labels`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          container_id: container.id,
          labels,
          delete_original: deleteOriginal,
        }),
      });
      await loadAllContainers();
      loadContainers();
    } catch (e) {
      row.querySelector(".warning").textContent = "Error: " + e.message;
    }
  };

  row.querySelector('[data-action="cancel"]').onclick = () => {
    renderAllContainers();
  };
}

async function removeThanosLabels(container) {
  if (
    !confirm(
      `Remove Thanos management from "${container.name}"? The container will be recreated.`,
    )
  )
    return;
  const labels = {
    "thanos.enabled": "",
    "thanos.display_name": "",
    "thanos.snap_timeout": "",
    "thanos.crash_detection": "",
    "thanos.notify_discord": "",
    "thanos.keep_running_on_boot": "",
  };
  try {
    await fetchJSON(`${API}/labels`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        container_id: container.id,
        labels,
        delete_original: true,
      }),
    });
    containerCache.delete(container.id);
    await loadAllContainers();
    loadContainers();
  } catch (e) {
    alert("Failed: " + e.message);
  }
}

// ── Log Viewer ──

const logModal = document.getElementById("logModal");
const logModalTitle = document.getElementById("logModalTitle");
const logOutput = document.getElementById("logOutput");
const statsBar = document.getElementById("statsBar");
let logWs = null;
let statsPollTimer = null;

function openLogViewer(id, card) {
  const name = cardName(card);
  logModalTitle.textContent = `Logs - ${name}`;
  logOutput.innerHTML = '<div class="loading">Connecting...</div>';
  statsBar.innerHTML = "";
  logModal.classList.add("active");

  // Close any existing connections.
  closeLogViewerConnections();

  // Build WebSocket URL with auth for log streaming only.
  const protocol = location.protocol === "https:" ? "wss:" : "ws:";
  const base = `${protocol}//${location.host}${API}/ws/`;
  const authParam = authToken ? `&token=${encodeURIComponent(authToken)}` : "";

  // Connect to log stream via WebSocket (logs require streaming).
  logWs = new WebSocket(`${base}logs?id=${id}${authParam}`);
  logWs.onmessage = (e) => {
    if (logOutput.querySelector(".loading")) {
      logOutput.innerHTML = "";
    }
    const msg = JSON.parse(e.data);
    if (msg.type === "log") {
      const line = document.createElement("div");
      line.className = "log-line";
      line.textContent = msg.data;
      logOutput.appendChild(line);
      logOutput.scrollTop = logOutput.scrollHeight;
      // Limit to 500 lines to prevent memory issues.
      while (logOutput.children.length > 500) {
        logOutput.removeChild(logOutput.firstChild);
      }
    }
  };
  logWs.onerror = () => {
    logOutput.innerHTML =
      '<div class="loading">Failed to connect to log stream.</div>';
  };

  // Poll stats via REST API (no streaming).
  const fetchStats = async () => {
    try {
      const stats = await fetchJSON(`${API}/containers/${id}/stats`);
      statsBar.innerHTML = `<span>CPU: ${stats.cpu}</span><span>MEM: ${stats.mem}</span>`;
    } catch (e) {
      // Ignore stats errors.
    }
  };
  fetchStats();
  statsPollTimer = setInterval(fetchStats, 5000);
}

function closeLogViewerConnections() {
  if (logWs) {
    logWs.close();
    logWs = null;
  }
  if (statsPollTimer) {
    clearInterval(statsPollTimer);
    statsPollTimer = null;
  }
}

document.getElementById("logModalClose").onclick = () => {
  logModal.classList.remove("active");
  closeLogViewerConnections();
};

logModal.addEventListener("click", (e) => {
  if (e.target === logModal) {
    logModal.classList.remove("active");
    closeLogViewerConnections();
  }
});

// ── State Log Viewer ──

// Opens the log modal and displays per-server state-change log entries
// fetched from the /api/server-logs/{id} endpoint.
function openStateLogViewer(id, card) {
  const name = cardName(card);
  logModalTitle.textContent = `State Logs - ${name}`;
  statsBar.innerHTML = "";
  logOutput.innerHTML = '<div class="loading">Loading state logs...</div>';
  logModal.classList.add("active");

  // Close any existing log viewer connections.
  closeLogViewerConnections();

  // Fetch state-change logs from the API.
  fetchJSON(`${API}/server-logs/${id}`)
    .then((data) => {
      const logs = data.logs || [];
      if (logs.length === 0) {
        logOutput.innerHTML =
          '<div class="loading">No state-change logs recorded yet.</div>';
        return;
      }
      logOutput.innerHTML = "";
      // Newest first (API returns DESC order). Render chronologically.
      logs.reverse().forEach((entry) => {
        const ts = new Date(entry.timestamp).toLocaleString();
        const el = document.createElement("div");
        el.className = "log-line";
        el.textContent = `[${ts}] ${entry.old_state} -> ${entry.new_state} | ${entry.blurb}`;
        logOutput.appendChild(el);
      });
      logOutput.scrollTop = logOutput.scrollHeight;
    })
    .catch((e) => {
      logOutput.innerHTML = `<div class="loading">Failed to load state logs: ${e.message}</div>`;
    });
}

// ── Traffic Viewer ──

// Opens the log modal and displays wake-on-connect events and known clients
// for a specific container, fetched from /api/traffic and /api/clients.
function openTrafficViewer(id, card) {
  const name = cardName(card);
  logModalTitle.textContent = `Traffic - ${name}`;
  statsBar.innerHTML = "";
  logOutput.innerHTML = '<div class="loading">Loading traffic data...</div>';
  logModal.classList.add("active");

  // Close any existing log viewer connections.
  closeLogViewerConnections();

  // Fetch both wake events and known clients in parallel.
  Promise.all([
    fetchJSON(`${API}/traffic?container=${encodeURIComponent(id)}&limit=100`),
    fetchJSON(`${API}/clients?container=${encodeURIComponent(id)}`),
  ])
    .then(([wakeData, clientData]) => {
      const wakes = wakeData.entries || [];
      const clients = clientData.entries || [];

      if (wakes.length === 0 && clients.length === 0) {
        logOutput.innerHTML =
          '<div class="loading">No traffic recorded yet.</div>';
        return;
      }

      let html = "";

      // Known clients section.
      if (clients.length > 0) {
        html += '<div class="traffic-section-title">Known Clients</div>';
        const [active, blocked] = splitByBlocked(clients);
        html += renderClientTable(active);
        if (blocked.length > 0) {
          html += renderCollapsible(
            `Blocked (${blocked.length})`,
            renderClientTable(blocked),
          );
        }
      }

      // Recent wake events section.
      if (wakes.length > 0) {
        html += '<div class="traffic-section-title">Recent Wake Events</div>';
        const [active, blocked] = splitByBlocked(wakes);
        html += renderWakeTable(active);
        if (blocked.length > 0) {
          html += renderCollapsible(
            `Blocked (${blocked.length})`,
            renderWakeTable(blocked),
          );
        }
      }

      logOutput.innerHTML = html;
    })
    .catch((e) => {
      logOutput.innerHTML = `<div class="loading">Failed to load traffic data: ${e.message}</div>`;
    });
}

// splitByBlocked partitions an array of traffic/client entries into
// [active, blocked] based on the boolean `blocked` field.
function splitByBlocked(entries) {
  const active = [];
  const blocked = [];
  for (const e of entries) {
    if (e.blocked) blocked.push(e);
    else active.push(e);
  }
  return [active, blocked];
}

// renderCollapsible wraps content in a <details> element so Blocked entries
// stay collapsed by default and can be expanded on demand (issue #7).
function renderCollapsible(summary, content) {
  return `<details class="traffic-collapsible">
    <summary>${escapeHTML(summary)}</summary>
    ${content}
  </details>`;
}

function renderClientTable(clients) {
  if (clients.length === 0) return "";
  let html = '<table class="traffic-table"><thead><tr>';
  html +=
    "<th>Source IP</th><th>Last Port</th><th>Packets</th><th>First Seen</th><th>Last Seen</th><th>Status</th>";
  html += "</tr></thead><tbody>";
  clients.forEach((c) => {
    const firstSeen = new Date(c.first_seen).toLocaleString();
    const lastSeen = timeAgo(new Date(c.last_seen));
    const blockedTag = c.blocked
      ? '<span class="badge-blocked">Blocked</span>'
      : "—";
    html += `<tr><td>${escapeHTML(c.src_ip)}</td><td>${c.last_port || "—"}</td>`;
    html += `<td>${c.pkt_count}</td><td>${firstSeen}</td><td>${lastSeen}</td><td>${blockedTag}</td></tr>`;
  });
  html += "</tbody></table>";
  return html;
}

function renderWakeTable(wakes) {
  if (wakes.length === 0) return "";
  let html = '<table class="traffic-table"><thead><tr>';
  html +=
    "<th>Time</th><th>Source IP</th><th>Dst Port</th><th>Protocol</th><th>Status</th>";
  html += "</tr></thead><tbody>";
  wakes.forEach((w) => {
    const ts = new Date(w.timestamp).toLocaleString();
    const blockedTag = w.blocked
      ? '<span class="badge-blocked">Blocked</span>'
      : "—";
    html += `<tr><td>${ts}</td><td>${escapeHTML(w.src_ip)}</td>`;
    html += `<td>${w.dst_port}</td><td>${escapeHTML(w.protocol)}</td><td>${blockedTag}</td></tr>`;
  });
  html += "</tbody></table>";
  return html;
}

// ── Init ──

// Bind global action handlers once at startup.
bindActions();

// If we have a stored token, try to use it. Otherwise show login.
if (authToken) {
  // Verify token is still valid.
  fetch(`${API}/containers`, { headers: authHeaders() })
    .then((r) => {
      if (r.ok) showDashboard();
      else showLogin();
    })
    .catch(() => showLogin());
} else {
  showLogin();
}

// Auto-refresh (only when dashboard is visible).
setInterval(() => {
  if (
    authToken &&
    !document.getElementById("dashboard").classList.contains("dashboard-hidden")
  ) {
    loadContainers();
    loadHealth();
  }
}, 5000);
