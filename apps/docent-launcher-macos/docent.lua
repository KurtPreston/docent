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
  wsmPort = tonumber(os.getenv("WSM_PORT")) or 39788,
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
local wsmBase = "http://127.0.0.1:" .. DOCENT.wsmPort

local chooser = nil

local function authHeaders(extra)
  local headers = extra or {}
  if DOCENT.token and DOCENT.token ~= "" then
    headers["Authorization"] = "Bearer " .. DOCENT.token
  end
  return headers
end

-- Render a rounded colored square for a work item's hex color as an hs.image,
-- shown as each chooser row's leading icon. Cached by hex so repeated summons
-- don't rebuild canvases.
local swatchCache = {}
local function swatchImage(hex)
  if not hex or hex == "" then hex = "#3A4060" end
  if swatchCache[hex] then return swatchCache[hex] end
  local c = hs.canvas.new({ x = 0, y = 0, w = 16, h = 16 })
  c[1] = {
    type = "rectangle",
    action = "fill",
    fillColor = { hex = hex },
    roundedRectRadii = { xRadius = 4, yRadius = 4 },
  }
  local img = c:imageFromCanvas()
  c:delete()
  swatchCache[hex] = img
  return img
end

local function workItemLabel(g)
  if g.repo and g.branch then return g.repo .. "  " .. g.branch end
  if g.repo then return g.repo end
  if g.ticket and g.summary and g.summary ~= "" then return g.ticket .. "  " .. g.summary end
  if g.ticket then return g.ticket end
  if g.key then return g.key end
  return "Work item"
end

local function workItemSubText(g)
  local parts = {}
  if g.ticket and g.repo and g.branch then table.insert(parts, g.ticket) end
  if g.summary and g.repo and g.branch then table.insert(parts, g.summary) end
  if g.status then table.insert(parts, g.status) end
  if g.jiraStatus then table.insert(parts, g.jiraStatus) end
  if g.openPath then table.insert(parts, g.openPath) end
  return table.concat(parts, "  ·  ")
end

local function workItemPath(key, action)
  return base .. "/api/workitems/" .. hs.http.encodeForQuery(key) .. "/" .. action
end

local function buildChoices(data, cb)
  local choices = {}
  for _, g in ipairs(data.groups or {}) do
    local ticket = g.ticket
    table.insert(choices, {
      text = workItemLabel(g),
      subText = workItemSubText(g),
      kind = "workitem",
      key = g.key,
      provider = data.provider,
      deepLink = g.deepLink,
      image = swatchImage(g.color),
      sort = g.needsFollowup and 0 or 1,
    })
    for _, s in ipairs(g.sessions or {}) do
      local subParts = {}
      if ticket then table.insert(subParts, ticket) end
      if s.host then table.insert(subParts, s.host) end
      if s.needsFollowup then table.insert(subParts, "● follow-up")
      elseif not s.live then table.insert(subParts, "closed") end
      table.insert(choices, {
        text = s.name,
        subText = table.concat(subParts, "  ·  "),
        kind = "session", name = s.name, host = s.host, image = swatchImage(s.color),
        sort = s.needsFollowup and 2 or (s.live and 3 or 4),
      })
    end
    for _, pr in ipairs(g.prs or {}) do
      table.insert(choices, {
        text = "PR #" .. tostring(pr.prNumber) .. "  " .. (pr.title or ""),
        subText = table.concat({ ticket or "", pr.repo or "", pr.state or "" }, "  ·  "),
        kind = "url", url = pr.url, image = swatchImage(g.color), sort = 5,
      })
    end
    if ticket and #(g.sessions or {}) == 0 and #(g.prs or {}) == 0 and g.jiraUrl then
      table.insert(choices, {
        text = ticket .. "  " .. (g.summary or ""),
        subText = g.jiraStatus or "",
        kind = "url", url = g.jiraUrl, image = swatchImage(g.color), sort = 6,
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
    hs.http.asyncPost(wsmBase .. "/focus", hs.json.encode(payload), { ["Content-Type"] = "application/json" },
      function(status, body, _)
        if status == 200 then return end
        local msg = (body and body ~= "") and body or ("HTTP " .. tostring(status))
        if msg:find("assistive access", 1, true) then
          msg = "Enable the wsm-macos binary in System Settings → Privacy & Security → Accessibility"
        end
        hs.notify.new({ title = "docent focus failed", informativeText = msg }):send()
      end)
  elseif choice.kind == "workitem" and choice.key then
    local function launchWorkItem()
      hs.http.asyncPost(
        workItemPath(choice.key, "launch"),
        "",
        authHeaders({ ["Content-Type"] = "application/json" }),
        function(status, body, _)
          if status >= 200 and status < 300 then return end
          local msg = "HTTP " .. tostring(status)
          if body and body ~= "" then
            local ok, data = pcall(hs.json.decode, body)
            if ok and data and data.error and data.error ~= "" then msg = data.error end
          end
          hs.notify.new({ title = "docent launch failed", informativeText = msg }):send()
        end
      )
    end

    if choice.provider == "cursor" or (choice.deepLink and choice.deepLink ~= "") then
      hs.http.asyncPost(
        workItemPath(choice.key, "open"),
        "",
        authHeaders({ ["Content-Type"] = "application/json" }),
        function(status, body, _)
          local link = choice.deepLink
          local msg = nil
          if body and body ~= "" then
            local ok, data = pcall(hs.json.decode, body)
            if ok and data then
              if data.deepLink and data.deepLink ~= "" then link = data.deepLink end
              if data.error and data.error ~= "" then msg = data.error end
            end
          end
          if link and link ~= "" then
            hs.urlevent.openURL(link)
            if status < 200 or status >= 300 then
              hs.notify.new({ title = "docent open warning", informativeText = msg or ("HTTP " .. tostring(status)) }):send()
            end
            return
          end
          launchWorkItem()
        end
      )
    else
      launchWorkItem()
    end
  elseif choice.kind == "url" and choice.url then
    hs.urlevent.openURL(choice.url)
  end
end

local function show()
  hs.http.asyncGet(base .. "/api/workitems", authHeaders(), function(status, body, _)
    local choices = {}
    if status == 200 and body then
      local ok, data = pcall(hs.json.decode, body)
      if ok and data then buildChoices(data, function(c) choices = c end) end
    elseif status ~= 200 then
      hs.notify.new({ title = "docent launcher", informativeText = "Could not load work items (HTTP " .. tostring(status) .. ")" }):send()
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
