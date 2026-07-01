-- docent-launcher-macos (Hammerspoon)
-- Install: copy to ~/.hammerspoon/docent.lua and `require("docent")` from init.lua
--
-- Spotlight-style chooser (default Ctrl+Alt+Space). Enter focuses a session or
-- opens a ticket/PR URL; Esc hides. The "Open ↗" toolbar button pops the full
-- dashboard into your system browser — when DOCENT.token is set it is forwarded
-- as a one-time ?token= query param, which the dashboard caches in
-- sessionStorage and strips from the address bar.

local DOCENT = {
  port = tonumber(os.getenv("DOCENT_PORT")) or 39787,
  wmPort = tonumber(os.getenv("DOCENT_WM_PORT")) or 39788,
  token = os.getenv("DOCENT_TOKEN"),
  hotkey = { mods = { "ctrl", "alt" }, key = "space" },
}

local launcher_cfg = (os.getenv("HOME") or "") .. "/.config/docent/launcher.lua"
local chunk = loadfile(launcher_cfg)
if chunk then
  local ok, overrides = pcall(chunk)
  if ok and type(overrides) == "table" then
    for k, v in pairs(overrides) do DOCENT[k] = v end
  end
end

local base = DOCENT.url or ("http://127.0.0.1:" .. DOCENT.port)
local wmBase = "http://127.0.0.1:" .. DOCENT.wmPort

local chooser = nil

local function buildChoices(data, cb)
  local choices = {}
  for _, g in ipairs(data.groups or {}) do
    local ticket = g.ticket
    for _, s in ipairs(g.sessions or {}) do
      local subParts = {}
      if ticket then table.insert(subParts, ticket) end
      if s.host then table.insert(subParts, s.host) end
      if s.needsFollowup then table.insert(subParts, "● follow-up")
      elseif not s.live then table.insert(subParts, "closed") end
      table.insert(choices, {
        text = s.name,
        subText = table.concat(subParts, "  ·  "),
        kind = "session", name = s.name, host = s.host, sort = s.needsFollowup and 0 or (s.live and 1 or 2),
      })
    end
    for _, pr in ipairs(g.prs or {}) do
      table.insert(choices, {
        text = "PR #" .. tostring(pr.prNumber) .. "  " .. (pr.title or ""),
        subText = table.concat({ ticket or "", pr.repo or "", pr.state or "" }, "  ·  "),
        kind = "url", url = pr.url, sort = 3,
      })
    end
    if ticket and #(g.sessions or {}) == 0 and #(g.prs or {}) == 0 and g.jiraUrl then
      table.insert(choices, {
        text = ticket .. "  " .. (g.summary or ""),
        subText = g.jiraStatus or "",
        kind = "url", url = g.jiraUrl, sort = 4,
      })
    end
  end
  table.sort(choices, function(a, b) return (a.sort or 9) < (b.sort or 9) end)
  cb(choices)
end

local function openDashboard()
  local url = base:gsub("/+$", "") .. "/"
  if DOCENT.token and DOCENT.token ~= "" then
    url = url .. "?token=" .. hs.http.encodeForQuery(DOCENT.token)
  end
  if chooser then chooser:hide() end
  hs.urlevent.openURL(url)
end

local function activate(choice)
  if not choice then return end
  if choice.kind == "session" then
    local payload = { name = choice.name }
    if choice.host then payload.host = choice.host end
    hs.http.asyncPost(wmBase .. "/focus", hs.json.encode(payload), { ["Content-Type"] = "application/json" },
      function(status, body, _)
        if status == 200 then return end
        local msg = (body and body ~= "") and body or ("HTTP " .. tostring(status))
        if msg:find("assistive access", 1, true) then
          msg = "Enable ~/.local/bin/docent-wm-macos in System Settings → Privacy & Security → Accessibility"
        end
        hs.notify.new({ title = "docent focus failed", informativeText = msg }):send()
      end)
  elseif choice.kind == "url" and choice.url then
    hs.urlevent.openURL(choice.url)
  end
end

local function show()
  hs.http.asyncGet(base .. "/sessions", nil, function(status, body, _)
    local choices = {}
    if status == 200 and body then
      local ok, data = pcall(hs.json.decode, body)
      if ok and data then buildChoices(data, function(c) choices = c end) end
    end
    if not chooser then
      chooser = hs.chooser.new(activate)
      chooser:searchSubText(true)
      local toolbar = hs.webview.toolbar.new("docentChooserToolbar")
        :addItems({
            { id = "NSToolbarFlexibleSpaceItem" },
            {
              id = "openDashboard",
              label = "Open ↗",
              tooltip = "Open the dashboard in your system browser",
              fn = function() openDashboard() end,
            },
          })
      chooser:attachedToolbar(toolbar)
    end
    chooser:choices(choices)
    chooser:query("")
    chooser:show()
  end)
end

hs.hotkey.bind(DOCENT.hotkey.mods, DOCENT.hotkey.key, show)
return DOCENT
