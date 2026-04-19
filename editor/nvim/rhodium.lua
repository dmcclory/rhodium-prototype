-- rhodium.lua — nvim plugin that renders review-state overlays for the PR
-- identified by g:rhodium_pr, and drives mark/unmark/note via the rhodium
-- CLI. Source of truth is the SQLite brain; this plugin is a thin view.
--
-- Load one of:
--   :lua require('rhodium')
--   (or drop the file under your runtimepath and `require` on VimEnter)
--
-- Activation: when g:rhodium_pr is set (e.g. via `nvim --cmd 'let
-- g:rhodium_pr="cli/cli#11569"'`), overlays render automatically.

local M = {}

local NS = vim.api.nvim_create_namespace("rhodium")
local SIGN_GROUP = "rhodium"
local CLI = vim.env.RHODIUM_BIN or "rhodium"

-- In-memory snapshot of the most-recent `rhodium state` response, keyed by
-- absolute path. Refreshed after every state-mutating action.
local state = {
  pr = nil,            -- e.g. "cli/cli#11569"
  files = {},          -- path → file record
  head_sha = "",
}

local function notify(msg, level)
  vim.notify("[rhodium] " .. msg, level or vim.log.levels.INFO)
end

local function shell(args)
  local out = vim.fn.systemlist(args)
  if vim.v.shell_error ~= 0 then
    return nil, table.concat(out, "\n")
  end
  return out, nil
end

-- fetch_state runs `rhodium state <pr>` and caches the result.
local function fetch_state()
  if not state.pr then return end
  local out, err = shell({ CLI, "state", state.pr })
  if not out then
    notify("state failed: " .. (err or ""), vim.log.levels.ERROR)
    return
  end
  local joined = table.concat(out, "\n")
  local ok, decoded = pcall(vim.json.decode, joined)
  if not ok then
    notify("state: bad json", vim.log.levels.ERROR)
    return
  end
  state.head_sha = decoded.head_sha or ""
  state.files = {}
  for _, f in ipairs(decoded.files or {}) do
    state.files[f.path] = f
  end
end

-- buffer_relpath returns the buffer's path relative to the worktree root (cwd
-- when nvim was launched from rhodium). We use forward-slashed paths to match
-- what GitHub / the brain store.
local function buffer_relpath(bufnr)
  local full = vim.api.nvim_buf_get_name(bufnr)
  if full == "" then return nil end
  local cwd = vim.fn.getcwd()
  if vim.startswith(full, cwd .. "/") then
    return full:sub(#cwd + 2)
  end
  return vim.fn.fnamemodify(full, ":.")
end

-- lookup_file returns the state record for the given buffer (or nil).
local function lookup_file(bufnr)
  local rel = buffer_relpath(bufnr)
  if not rel then return nil end
  return state.files[rel], rel
end

local SIGN_UNREVIEWED = "rhodium_unreviewed"
local SIGN_REVIEWED = "rhodium_reviewed"

local function ensure_signs()
  vim.fn.sign_define(SIGN_UNREVIEWED, { text = "!", texthl = "DiagnosticWarn" })
  vim.fn.sign_define(SIGN_REVIEWED,   { text = "✓", texthl = "DiagnosticOk"   })
end

-- render places signs and virtual-text overlays for `file` in `bufnr`.
local function render(bufnr)
  vim.api.nvim_buf_clear_namespace(bufnr, NS, 0, -1)
  vim.fn.sign_unplace(SIGN_GROUP, { buffer = bufnr })

  local file = lookup_file(bufnr)
  if not file then return end

  local line_count = vim.api.nvim_buf_line_count(bufnr)

  for _, h in ipairs(file.hunks or {}) do
    local line = h.new_line
    if line and line >= 1 and line <= line_count then
      local sign = h.marked and SIGN_REVIEWED or SIGN_UNREVIEWED
      vim.fn.sign_place(0, SIGN_GROUP, sign, bufnr, { lnum = line, priority = 10 })
      local tag = h.marked and "[reviewed]" or "[unreviewed]"
      local hl  = h.marked and "DiagnosticOk" or "DiagnosticWarn"
      vim.api.nvim_buf_set_extmark(bufnr, NS, line - 1, 0, {
        virt_text = { { "  " .. tag, hl } },
        virt_text_pos = "eol",
      })
    end
  end

  for _, n in ipairs(file.notes or {}) do
    local line = n.line_no
    if line and line >= 1 and line <= line_count then
      local body = (n.body or ""):gsub("\n", " ")
      vim.api.nvim_buf_set_extmark(bufnr, NS, line - 1, 0, {
        virt_lines = { { { "  [note] " .. body, "Comment" } } },
        virt_lines_above = true,
      })
    end
  end
end

-- render_all refreshes overlays across every rhodium-tracked buffer.
local function render_all()
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    if vim.api.nvim_buf_is_loaded(b) then
      render(b)
    end
  end
end

-- hunk_at returns the hunk under the cursor in bufnr (or nil).
local function hunk_at(bufnr, lnum)
  local file = lookup_file(bufnr)
  if not file then return nil end
  -- Pick the hunk whose new_line is the greatest <= lnum; approximates
  -- "cursor is within this hunk's region". Good enough for MVP.
  local best
  for _, h in ipairs(file.hunks or {}) do
    if h.new_line and h.new_line <= lnum then
      if not best or h.new_line > best.new_line then
        best = h
      end
    end
  end
  return best
end

local function current_hunk()
  local bufnr = vim.api.nvim_get_current_buf()
  local lnum = vim.api.nvim_win_get_cursor(0)[1]
  return hunk_at(bufnr, lnum), bufnr
end

local function mark(on)
  local h, bufnr = current_hunk()
  if not h then
    notify("no hunk under cursor")
    return
  end
  local _, rel = lookup_file(bufnr)
  local verb = on and "mark" or "unmark"
  local _, err = shell({ CLI, verb, state.pr, rel, h.hash })
  if err then
    notify(verb .. " failed: " .. err, vim.log.levels.ERROR)
    return
  end
  fetch_state()
  render_all()
end

local function jump(direction)
  local file = lookup_file(vim.api.nvim_get_current_buf())
  if not file then return end
  local lnum = vim.api.nvim_win_get_cursor(0)[1]
  local candidates = {}
  for _, h in ipairs(file.hunks or {}) do
    if not h.marked and h.new_line then
      table.insert(candidates, h.new_line)
    end
  end
  table.sort(candidates)
  local target
  if direction > 0 then
    for _, n in ipairs(candidates) do
      if n > lnum then target = n; break end
    end
  else
    for i = #candidates, 1, -1 do
      if candidates[i] < lnum then target = candidates[i]; break end
    end
  end
  if target then
    vim.api.nvim_win_set_cursor(0, { target, 0 })
  else
    notify(direction > 0 and "no next unreviewed hunk" or "no previous unreviewed hunk")
  end
end

local function add_note()
  local bufnr = vim.api.nvim_get_current_buf()
  local _, rel = lookup_file(bufnr)
  if not rel then
    notify("buffer not in the PR")
    return
  end
  local lnum = vim.api.nvim_win_get_cursor(0)[1]
  vim.ui.input({ prompt = string.format("note on %s:%d: ", rel, lnum) }, function(body)
    if not body or body == "" then return end
    local _, err = shell({ CLI, "note", state.pr, rel, tostring(lnum), body })
    if err then
      notify("note failed: " .. err, vim.log.levels.ERROR)
      return
    end
    fetch_state()
    render_all()
  end)
end

local function next_unreviewed_file()
  for _, f in pairs(state.files) do
    if f.status ~= "seen" then
      vim.cmd("edit " .. vim.fn.fnameescape(f.path))
      return
    end
  end
  notify("all files reviewed")
end

local function setup_keymaps(bufnr)
  local opts = { buffer = bufnr, silent = true }
  vim.keymap.set("n", "]h", function() jump(1) end, opts)
  vim.keymap.set("n", "[h", function() jump(-1) end, opts)
  vim.keymap.set("n", "<leader>rm", function() mark(true) end, opts)
  vim.keymap.set("n", "<leader>ru", function() mark(false) end, opts)
  vim.keymap.set("n", "<leader>rn", add_note, opts)
  vim.keymap.set("n", "<leader>rN", next_unreviewed_file, opts)
end

function M.setup()
  if vim.g.rhodium_pr == nil or vim.g.rhodium_pr == "" then
    return
  end
  state.pr = vim.g.rhodium_pr
  ensure_signs()
  fetch_state()

  local group = vim.api.nvim_create_augroup("rhodium", { clear = true })
  vim.api.nvim_create_autocmd({ "BufReadPost", "BufEnter" }, {
    group = group,
    callback = function(args)
      if lookup_file(args.buf) then
        setup_keymaps(args.buf)
        render(args.buf)
      end
    end,
  })

  -- Explicit refresh command for manual use.
  vim.api.nvim_create_user_command("RhodiumRefresh", function()
    fetch_state()
    render_all()
  end, {})

  -- Paint whatever's already loaded (the file nvim was launched with).
  for _, b in ipairs(vim.api.nvim_list_bufs()) do
    if vim.api.nvim_buf_is_loaded(b) and lookup_file(b) then
      setup_keymaps(b)
      render(b)
    end
  end
end

-- auto-run when the module is required
M.setup()

return M
