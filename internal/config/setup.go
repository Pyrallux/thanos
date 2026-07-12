package config

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// setupRequest is the JSON body POSTed from the /setup wizard form.
type setupRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	DiscordBotToken  string `json:"discord_bot_token"`
	DiscordGuildID   string `json:"discord_guild_id"`
	DiscordChannelID string `json:"discord_channel_id"`
	NetworkInterface string `json:"network_interface"`
}

// runSetupWizard serves a temporary HTTP server on :4040, waits for the
// user to complete the wizard, persists the config, and returns.
func runSetupWizard(ctx context.Context, db *sql.DB, cfg *Config) error {
	doneCh := make(chan setupRequest, 1)

	mux := http.NewServeMux()

	mux.HandleFunc("/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Minimal inline wizard so no external static files are needed.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, setupPage)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req setupRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if req.Username == "" || req.Password == "" {
			http.Error(w, "username and password are required", http.StatusBadRequest)
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, "failed to hash password", http.StatusInternalServerError)
			return
		}

		// Persist all config rows.
		cfg.WebUsername = req.Username
		cfg.WebPasswordHash = string(hash)
		cfg.DiscordBotToken = req.DiscordBotToken
		cfg.DiscordGuildID = req.DiscordGuildID
		cfg.DiscordChannelID = req.DiscordChannelID
		cfg.NetworkInterface = req.NetworkInterface
		cfg.APIPort = 4040

		if err := cfg.SaveWebAuth(); err != nil {
			http.Error(w, "save web auth: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := cfg.SaveDiscord(); err != nil {
			http.Error(w, "save discord: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := cfg.SaveKV("network_interface", req.NetworkInterface); err != nil {
			http.Error(w, "save iface: "+err.Error(), http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
		doneCh <- req
	})

	mux.HandleFunc("/api/interfaces", func(w http.ResponseWriter, r *http.Request) {
		out, err := ListNetworkInterfaces()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})

	srv := &http.Server{Addr: ":4040", Handler: mux}
	go func() {
		slog.Info("setup wizard available at http://localhost:4040/setup")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("setup wizard server error", "err", err)
		}
	}()
	defer srv.Shutdown(context.Background())

	select {
	case <-doneCh:
		// Give the browser a moment, then return so main can start the real server.
		time.Sleep(500 * time.Millisecond)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

const setupPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Thanos Setup</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 520px; margin: 60px auto; padding: 0 20px; background:#2a1a3e; color:#e8e0f0; }
  h1 { margin-bottom: 4px; color: #d4af37; }
  label { display:block; margin-top:14px; font-weight:600; }
  input, select { width:100%; padding:8px; margin-top:4px; box-sizing:border-box; background:#3d2160; color:#e8e0f0; border:1px solid #5a3a80; border-radius:4px; }
  input:focus { border-color: #8a6fd1; outline: none; }
  button { margin-top:24px; padding:10px 22px; cursor:pointer; background:#451d70; color:#e8e0f0; border:1px solid #5a3a80; border-radius:6px; font-size:1rem; }
  button:hover { background:#8a6fd1; }
  button:disabled { opacity:0.5; cursor:wait; }
  .hint { color:#8a7ba0; font-size:13px; margin-top:4px; }
  .section-title { margin-top:28px; font-size:1.1rem; font-weight:700; border-bottom:1px solid #5a3a80; padding-bottom:6px; color:#d4af37; }
  .saved-screen { text-align:center; padding:40px 0; }
  .saved-screen .check { font-size:3rem; }
  .saved-screen h2 { margin:16px 0 8px; color:#d4af37; }
  .saved-screen p { color:#8a7ba0; }
  .error-msg { color:#f44336; }
  input.invalid { border-color:#f44336 !important; box-shadow:0 0 0 1px #f44336; }
  #form { display:block; }
  #saved { display:none; }
</style>
</head>
<body>
<div id="form">
  <h1>Thanos</h1>
  <p class="hint">First-run setup wizard. This page is only available before credentials are configured.</p>

  <div class="section-title">Create Web UI Login</div>
  <label>Username (new)</label>
  <input id="username" autocomplete="off">
  <label>Password (new)</label>
  <input id="password" type="password" autocomplete="new-password">
  <label>Confirm Password</label>
  <input id="password2" type="password" autocomplete="new-password">

  <div class="section-title">Discord (Optional)</div>
  <label>Bot Token</label>
  <input id="bot_token" autocomplete="off">
  <label>Guild ID</label>
  <input id="guild_id" autocomplete="off">
  <label>Channel ID</label>
  <input id="channel_id" autocomplete="off">

  <div class="section-title">Network</div>
  <label>Network Interface (for packet sniffing)</label>
  <select id="iface"></select>

  <button id="save">Save &amp; Start</button>
  <p id="msg" class="hint"></p>
</div>

<div id="saved" class="saved-screen">
  <div class="check">✅</div>
  <h2>Setup Complete!</h2>
  <p>Thanos is now running. The dashboard will load shortly.<br>
  Use the username and password you just set to log in.</p>
</div>

<script>
async function loadIfaces(){
  const r = await fetch('/api/interfaces');
  const list = await r.json();
  const sel = document.getElementById('iface');
  list.forEach(i => {
    const o = document.createElement('option');
    o.value = i.name; o.text = i.name + ' (' + (i.addrs||[]).join(', ') + ')';
    sel.add(o);
  });
}
loadIfaces();

// Real-time validation: clear red outline when user edits.
['username', 'password', 'password2'].forEach(id => {
  const el = document.getElementById(id);
  el.addEventListener('input', () => el.classList.remove('invalid'));
});

document.getElementById('save').onclick = async () => {
  const msg = document.getElementById('msg');
  const userEl = document.getElementById('username');
  const passEl = document.getElementById('password');
  const pass2El = document.getElementById('password2');
  const user = userEl.value;
  const pass = passEl.value;
  const pass2 = pass2El.value;

  // Clear previous validation state.
  [userEl, passEl, pass2El].forEach(el => el.classList.remove('invalid'));
  msg.textContent = '';
  msg.className = 'hint';

  let valid = true;
  if (!user) { userEl.classList.add('invalid'); valid = false; }
  if (!pass) { passEl.classList.add('invalid'); valid = false; }
  if (!pass2) { pass2El.classList.add('invalid'); valid = false; }

  if (!valid) {
    msg.textContent = 'Please fill in all required fields (highlighted in red).';
    msg.className = 'hint error-msg';
    return;
  }
  if (pass !== pass2) {
    passEl.classList.add('invalid');
    pass2El.classList.add('invalid');
    msg.textContent = 'Passwords do not match.';
    msg.className = 'hint error-msg';
    return;
  }
  if (pass.length < 4) {
    passEl.classList.add('invalid');
    msg.textContent = 'Password must be at least 4 characters.';
    msg.className = 'hint error-msg';
    return;
  }

  const btn = document.getElementById('save');
  btn.disabled = true;
  btn.textContent = 'Saving...';

  const body = {
    username: user,
    password: pass,
    discord_bot_token: document.getElementById('bot_token').value,
    discord_guild_id: document.getElementById('guild_id').value,
    discord_channel_id: document.getElementById('channel_id').value,
    network_interface: document.getElementById('iface').value,
  };
  try {
    const r = await fetch('/setup', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify(body)});
    if (r.ok) {
      document.getElementById('form').style.display = 'none';
      document.getElementById('saved').style.display = 'block';
      setTimeout(() => { window.location.href = '/'; }, 2000);
    } else {
      const text = await r.text();
      msg.textContent = 'Error: ' + text;
      msg.className = 'hint error-msg';
      btn.disabled = false;
      btn.textContent = 'Save & Start';
    }
  } catch(e) {
    msg.textContent = 'Error: ' + e.message;
    msg.className = 'hint error-msg';
    btn.disabled = false;
    btn.textContent = 'Save & Start';
  }
};
</script>
</body>
</html>`