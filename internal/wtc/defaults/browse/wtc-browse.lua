-- wtc-browse.lua — one LazyVim, one vim tab per sibling (`:tcd`).
--
-- Tab 1 (`diff`) is a change index across every sibling, against the
-- branch-off point (merge-base with the PR base / default_ref) — not
-- the current tip of that target. If origin/main (or the PR base) has
-- moved, the tree shows ↓ catch-up instead of mixing those commits
-- into the file list.
--
-- The right pane (and each repo tab) shows the real file with
-- unified.nvim's inline overlay — not a raw `ft=diff` patch. <CR> in
-- the tree peeks that file. `o` opens it on the repo tab. <leader>gd
-- returns to the file list. <leader>gD opens Diffview (side-by-side).

local root = vim.g.wtc_browse_root
if type(root) ~= "string" or root == "" then
  root = vim.fn.getcwd()
end
root = vim.fs.normalize(root)

-- nvim 0.12 + snacks.scope/indent: treesitter :parse(injections) on
-- BufReadPost (especially markdown) calls node:range() on nil. Browse
-- does not need indent guides or ii/ai scope — keep them off.
vim.g.snacks_scope = false
vim.g.snacks_indent = false

local started = false
local apply_inline_diff
local open_changed_file
local fill_hunk_buf
local reveal_hunk
local watch_timer
local ref_cache = {}
local last_tree_dir, last_tree_source
local tabline_hl_done = false
local buf_meta = {} -- bufnr → { index, snapshots, hunk_at } — keep off vim.b (slow / E5101)

local function git(cwd, args)
  local cmd = { "git", "-C", cwd }
  vim.list_extend(cmd, args)
  local r = vim.system(cmd, { text = true }):wait()
  if r.code ~= 0 then
    return ""
  end
  return ((r.stdout or ""):gsub("\n$", ""))
end

local function worktrees()
  local found = {}
  local fd = vim.uv.fs_scandir(root)
  if not fd then
    return found
  end
  while true do
    local name, typ = vim.uv.fs_scandir_next(fd)
    if not name then
      break
    end
    if typ == "directory" and not name:match("^%.") then
      local path = root .. "/" .. name
      if vim.uv.fs_stat(path .. "/.git") then
        found[#found + 1] = { name = name, path = path }
      end
    end
  end
  table.sort(found, function(a, b)
    return a.name < b.name
  end)
  return found
end

local function default_ref_for(dir_name)
  local command = vim.g.wtc_base_command
  if type(command) ~= "string" or command == "" then
    error("browse needs its serving CLI to resolve repository bases")
  end
  local r = vim.system({ command, "--json", "repo", "base", "show", dir_name,
    "--collection", root }, { text = true }):wait()
  if r.code ~= 0 then
    error("cannot resolve receiving repository base: " .. (r.stderr or "unknown error"))
  end
  local ok, result = pcall(vim.json.decode, r.stdout or "")
  if not ok or not result.data or not result.data.effective_ref then
    error("invalid repository base result from serving CLI")
  end
  return result.data.effective_ref
end

local function compare_ref(wt, dir_name)
  local hit = ref_cache[wt]
  if hit and (vim.uv.now() - hit.t) < 60000 then
    return hit.ref, hit.kind
  end
  local ref, kind = default_ref_for(dir_name), "tip"
  local branch = git(wt, { "branch", "--show-current" })
  if branch ~= "" and vim.fn.executable("gh") == 1 then
    local r = vim.system({
      "gh", "pr", "view", "--json", "baseRefName", "-q", ".baseRefName",
    }, { cwd = wt, text = true }):wait()
    if r.code == 0 then
      local base = (r.stdout or ""):gsub("%s+", "")
      if base ~= "" then
        ref, kind = "origin/" .. base, "pr"
      end
    end
  end
  ref_cache[wt] = { ref = ref, kind = kind, t = vim.uv.now() }
  return ref, kind
end

local function merge_base(wt, ref)
  local mb = git(wt, { "merge-base", "HEAD", ref })
  if mb == "" then
    return ref
  end
  return mb
end

local function count_revs(wt, range)
  local s = git(wt, { "rev-list", "--count", range })
  s = (s:gsub("%s+", ""))
  return tonumber(s) or 0
end

-- Paths dirty vs HEAD (staged or not). Untracked is handled separately.
local function dirty_paths(wt)
  local dirty = {}
  local porcelain = git(wt, { "status", "--porcelain=v1", "-uno" })
  if porcelain == "" then
    return dirty
  end
  for line in (porcelain .. "\n"):gmatch("([^\n]*)\n") do
    local code, rest = line:match("^(..) (.*)$")
    if code and rest and code ~= "??" then
      dirty[rest:match(" -> (.+)$") or rest] = true
    end
  end
  return dirty
end

-- Changes since the branch-off point (merge-base with the merge target),
-- including the worktree. The target tip is only used for ↓ catch-up.
local function collect_changes(tree)
  local ref, kind = compare_ref(tree.path, tree.name)
  local mb = merge_base(tree.path, ref)
  local ahead = count_revs(tree.path, ref .. "..HEAD")
  local behind = count_revs(tree.path, "HEAD.." .. ref)
  local dirty = dirty_paths(tree.path)
  local files = {}
  local numstat = git(tree.path, { "diff", "--numstat", mb })
  if numstat ~= "" then
    for line in (numstat .. "\n"):gmatch("([^\n]*)\n") do
      local add, del, path = line:match("^(%S+)\t(%S+)\t(.+)$")
      if path then
        -- rename: "old => new"
        local renamed = path:match(" => (.+)$")
        local p = renamed or path
        files[#files + 1] = {
          path = p,
          add = add == "-" and 0 or tonumber(add) or 0,
          del = del == "-" and 0 or tonumber(del) or 0,
          kind = dirty[p] and "dirty" or "committed",
        }
      end
    end
  end
  local untracked = git(tree.path, { "ls-files", "-o", "--exclude-standard" })
  if untracked ~= "" then
    for line in (untracked .. "\n"):gmatch("([^\n]*)\n") do
      if line ~= "" then
        files[#files + 1] = { path = line, add = 0, del = 0, kind = "untracked" }
      end
    end
  end
  return {
    tree = tree,
    ref = ref,
    kind = kind,
    mb = mb,
    ahead = ahead,
    behind = behind,
    files = files,
  }
end

local tree_ns = vim.api.nvim_create_namespace("wtc_ctree")

local function file_mtime(path)
  local st = vim.uv.fs_stat(path)
  if not st or not st.mtime then
    return 0
  end
  if type(st.mtime) == "table" then
    return (st.mtime.sec or 0) + (st.mtime.nsec or 0) / 1e9
  end
  return st.mtime
end

local function snapshots_all()
  local snaps = {}
  for _, t in ipairs(worktrees()) do
    snaps[#snaps + 1] = collect_changes(t)
  end
  return snaps
end

local function fingerprint(snaps)
  -- File set + line counts only. Do not include mtime — a write every
  -- second would rebuild the hunk buffer and hitch scrolling.
  local parts = {}
  for _, s in ipairs(snaps) do
    for _, f in ipairs(s.files) do
      parts[#parts + 1] = table.concat({
        s.tree.name, f.path, f.kind, f.add, f.del,
      }, "\t")
    end
    parts[#parts + 1] = table.concat({
      s.tree.name, "meta", s.ahead or 0, s.behind or 0, s.mb or "",
    }, "\t")
  end
  table.sort(parts)
  return table.concat(parts, "\n")
end

local function newest_edit(snaps, after)
  local best, best_t = nil, after or 0
  local now = os.time()
  for _, s in ipairs(snaps) do
    for _, f in ipairs(s.files) do
      local abs = s.tree.path .. "/" .. f.path
      local t = file_mtime(abs)
      if t > best_t and (now - t) < 30 then
        best_t = t
        best = { repo = s.tree.name, path = f.path, mb = s.mb, abs = abs }
      end
    end
  end
  return best, best_t
end

local function nest_changes(snaps)
  local top = {}
  for _, snap in ipairs(snaps) do
    local repo = snap.tree.name
    top[repo] = top[repo] or {
      name = repo,
      kids = {},
      clean = #snap.files == 0,
      ref = snap.ref,
      mb = snap.mb,
      ahead = snap.ahead or 0,
      behind = snap.behind or 0,
    }
    local root_node = top[repo]
    root_node.clean = #snap.files == 0
    root_node.ref = snap.ref
    root_node.mb = snap.mb
    root_node.ahead = snap.ahead or 0
    root_node.behind = snap.behind or 0
    for _, f in ipairs(snap.files) do
      local node = root_node.kids
      local parts = {}
      for p in f.path:gmatch("[^/]+") do
        parts[#parts + 1] = p
      end
      for i, p in ipairs(parts) do
        node[p] = node[p] or { name = p, kids = {}, file = nil }
        if i == #parts then
          node[p].file = f
          node[p].repo = repo
          node[p].mb = snap.mb
        else
          node = node[p].kids
        end
      end
    end
  end
  return top
end

local function sorted_names(map)
  local ks = {}
  for k, v in pairs(map) do
    ks[#ks + 1] = { k = k, dir = v.file == nil }
  end
  table.sort(ks, function(a, b)
    if a.dir ~= b.dir then
      return a.dir
    end
    return a.k < b.k
  end)
  local out = {}
  for _, x in ipairs(ks) do
    out[#out + 1] = x.k
  end
  return out
end

local function flatten_tree(top)
  local lines, meta, hls = {}, {}, {}
  local function add(text, info, kind, virt)
    lines[#lines + 1] = text
    if info then
      meta[#lines] = info
    end
    hls[#hls + 1] = { #lines, kind, virt }
  end
  local function walk(map, indent)
    for _, name in ipairs(sorted_names(map)) do
      local node = map[name]
      if node.file then
        local f = node.file
        local virt
        if f.kind == "untracked" then
          virt = { { "?", "DiagnosticHint" } }
        else
          virt = {}
          if f.kind == "dirty" then
            virt[#virt + 1] = { "* ", "DiagnosticWarn" }
          end
          virt[#virt + 1] = { "+" .. tostring(f.add), "DiffAdd" }
          virt[#virt + 1] = { " ", "Normal" }
          virt[#virt + 1] = { "−" .. tostring(f.del), "DiffDelete" }
        end
        add(indent .. name, {
          repo = node.repo,
          path = f.path,
          mb = node.mb,
          kind = "file",
        }, f.kind, virt)
      elseif node.ref then
        -- Path only on the left; since / ↑ / ↓ sit on the right.
        local virt = {}
        virt[#virt + 1] = {
          "since " .. (node.mb and node.mb:sub(1, 7) or "?"),
          "Comment",
        }
        if (node.ahead or 0) > 0 then
          virt[#virt + 1] = { "  ↑" .. node.ahead, "Comment" }
        end
        if (node.behind or 0) > 0 then
          virt[#virt + 1] = { "  ↓" .. node.behind, "DiagnosticWarn" }
        elseif node.clean then
          virt[#virt + 1] = { "  clean", "Comment" }
        end
        add("▾ " .. name, { repo = name, kind = "repo", mb = node.mb }, "repo", virt)
        if (node.behind or 0) > 0 then
          local n = node.behind
          local noun = n == 1 and "commit" or "commits"
          add(string.format("  ↓ catch-up  %s is %d %s ahead", node.ref, n, noun), {
            repo = name,
            kind = "catchup",
          }, "catchup")
        end
        walk(node.kids, indent .. "  ")
      else
        add(indent .. "▾ " .. name, { kind = "dir" }, "dir")
        walk(node.kids, indent .. "  ")
      end
    end
  end
  walk(top, "")
  return lines, meta, hls
end

local function load_file_in_win(win, path)
  local buf = vim.fn.bufadd(path)
  pcall(function()
    vim.b[buf].snacks_scope = false
    vim.b[buf].snacks_indent = false
  end)
  pcall(vim.fn.bufload, buf)
  if vim.api.nvim_win_is_valid(win) and vim.api.nvim_buf_is_valid(buf) then
    vim.api.nvim_win_set_buf(win, buf)
  end
  return buf
end

local function peek_change(repo, rel, mb)
  local win = rawget(vim.t, "wtc_peek_win")
  if not (win and vim.api.nvim_win_is_valid(win)) then
    return
  end
  local path = root .. "/" .. repo .. "/" .. rel
  if not vim.uv.fs_stat(path) then
    return
  end
  local cur = vim.api.nvim_buf_get_name(vim.api.nvim_win_get_buf(win))
  if vim.bo[vim.api.nvim_win_get_buf(win)].modified and cur ~= path then
    return
  end
  local buf
  if cur == path then
    buf = vim.api.nvim_win_get_buf(win)
    pcall(vim.cmd.checktime)
  else
    buf = load_file_in_win(win, path)
  end
  apply_inline_diff(mb, buf, win)
end

local function render_change_tree(buf, snaps)
  local lines, meta, hls = flatten_tree(nest_changes(snaps))
  if #lines == 0 then
    lines = { "  (no changes)" }
  end
  local follow = rawget(vim.t, "wtc_follow")
  if follow == nil then
    follow = true
  end
  table.insert(lines, 1, follow and "follow on   f toggle  <CR> peek  o open" or "follow off  f toggle  <CR> peek  o open")
  -- shift meta by 1
  local shifted = {}
  for i, m in pairs(meta) do
    if type(i) == "number" then
      shifted[i + 1] = m
    end
  end
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.api.nvim_buf_clear_namespace(buf, tree_ns, 0, -1)
  pcall(vim.api.nvim_buf_set_extmark, buf, tree_ns, 0, 0, {
    end_col = #lines[1],
    hl_group = "Comment",
  })
  for _, h in ipairs(hls) do
    local lnum = h[1] + 1 -- header offset
    local kind = h[2]
    local virt = h[3]
    local line = lines[lnum] or ""
    local row_hl = (kind == "repo" and "Title")
      or (kind == "dir" and "Directory")
      or (kind == "catchup" and "DiagnosticWarn")
      or nil
    if row_hl then
      pcall(vim.api.nvim_buf_set_extmark, buf, tree_ns, lnum - 1, 0, {
        end_col = #line,
        hl_group = row_hl,
      })
    end
    if type(virt) == "table" and #virt > 0 then
      pcall(vim.api.nvim_buf_set_extmark, buf, tree_ns, lnum - 1, 0, {
        virt_text = virt,
        virt_text_pos = "right_align",
        hl_mode = "combine",
      })
    end
  end
  vim.bo[buf].modifiable = false
  buf_meta[buf] = buf_meta[buf] or {}
  buf_meta[buf].index = shifted
  buf_meta[buf].snapshots = snaps
end

local function tree_node_at_cursor(buf)
  local lnum = vim.api.nvim_win_get_cursor(0)[1]
  local meta = (buf_meta[buf] or {}).index or {}
  return meta[lnum] or meta[tostring(lnum)]
end

local function refresh_collection_tab(tab)
  if not tab or not vim.api.nvim_tabpage_is_valid(tab) then
    return
  end
  local ok_kind, kind = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_kind")
  if not ok_kind or kind ~= "diff-all" then
    return
  end
  -- Never steal the UI while the user is inserting or mid-operator.
  local mode = vim.fn.mode()
  if mode ~= "n" and mode ~= "nt" then
    return
  end
  local tbuf_ok, tbuf = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_tree_buf")
  if not tbuf_ok or not tbuf or not vim.api.nvim_buf_is_valid(tbuf) then
    return
  end
  local snaps = snapshots_all()
  local fp = fingerprint(snaps)
  local prev = select(2, pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_fp"))
  local follow = select(2, pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_follow"))
  if follow == nil then
    follow = true
  end
  -- Do not switch tabpages and do not rebuild the hunk buffer on the
  -- timer — both hitch scrolling. Tree update is cheap; hunks refresh on `r`.
  if fp ~= prev then
    pcall(vim.api.nvim_tabpage_set_var, tab, "wtc_fp", fp)
    render_change_tree(tbuf, snaps)
  end
  if follow then
    local last = select(2, pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_follow_last")) or 0
    local hit, t = newest_edit(snaps, last)
    if hit then
      pcall(vim.api.nvim_tabpage_set_var, tab, "wtc_follow_last", math.floor(t))
      reveal_hunk(hit.repo, hit.path, hit.mb)
    end
  end
end

local function hunk_section(tree)
  local ref = select(1, compare_ref(tree.path, tree.name))
  local mb = merge_base(tree.path, ref)
  local ahead = count_revs(tree.path, ref .. "..HEAD")
  local behind = count_revs(tree.path, "HEAD.." .. ref)
  local patch = git(tree.path, { "diff", "--no-ext-diff", mb })
  local untracked = git(tree.path, { "ls-files", "-o", "--exclude-standard" })
  local status
  if behind > 0 then
    status = string.format("↑%d  ↓%d %s — catch-up", ahead, behind, ref)
  elseif ahead > 0 then
    status = string.format("↑%d  target %s", ahead, ref)
  else
    status = "target " .. ref
  end
  local lines = {
    string.format("# %s  since %s  %s", tree.name, mb:sub(1, 7), status),
    "",
  }
  local marks = { [tree.name] = 1 }
  if behind > 0 then
    local n = behind
    local noun = n == 1 and "commit" or "commits"
    lines[#lines + 1] = string.format("# ↓ catch-up  %s is %d %s ahead", ref, n, noun)
    marks[tree.name .. "/catchup"] = #lines
    local log = git(tree.path, { "log", "--oneline", "HEAD.." .. ref })
    if log ~= "" then
      for line in (log .. "\n"):gmatch("([^\n]*)\n") do
        if line ~= "" then
          lines[#lines + 1] = "#   " .. line
        end
      end
    end
    lines[#lines + 1] = "#   click catch-up in the tree to open the incoming diff"
    lines[#lines + 1] = ""
  end
  if patch == "" and untracked == "" then
    if behind == 0 then
      lines[#lines + 1] = "# clean"
      lines[#lines + 1] = ""
    end
    return lines, marks
  end
  if patch ~= "" then
    for line in (patch .. "\n"):gmatch("([^\n]*)\n") do
      lines[#lines + 1] = line
      local p = line:match("^diff %-%-git a/.+ b/(.+)$")
      if p and p ~= "/dev/null" then
        marks[tree.name .. "/" .. p] = #lines
      end
    end
    lines[#lines + 1] = ""
  end
  if untracked ~= "" then
    lines[#lines + 1] = "# untracked"
    for line in (untracked .. "\n"):gmatch("([^\n]*)\n") do
      if line ~= "" then
        lines[#lines + 1] = "#   " .. line
      end
    end
    lines[#lines + 1] = ""
  end
  return lines, marks
end

function fill_hunk_buf(buf)
  local lines = {
    "# " .. vim.fs.basename(root) .. "  — hunks since branch-off (not the merge-target tip)",
    "# ↓ catch-up in the tree means the target moved ahead. click a file to jump here",
    "",
  }
  local marks = {}
  for _, t in ipairs(worktrees()) do
    local part, m = hunk_section(t)
    local offset = #lines
    vim.list_extend(lines, part)
    for k, lnum in pairs(m) do
      marks[k] = lnum + offset
    end
  end
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.bo[buf].modifiable = false
  vim.bo[buf].filetype = "diff"
  buf_meta[buf] = buf_meta[buf] or {}
  buf_meta[buf].hunk_at = marks
end

local function collection_tab()
  local tab = vim.api.nvim_get_current_tabpage()
  local ok, kind = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_kind")
  if ok and kind == "diff-all" then
    return tab
  end
  for _, t in ipairs(vim.api.nvim_list_tabpages()) do
    local k
    ok, k = pcall(vim.api.nvim_tabpage_get_var, t, "wtc_kind")
    if ok and k == "diff-all" then
      return t
    end
  end
  return nil
end

local function show_hunk_overview()
  local tab = collection_tab()
  if not tab then
    return nil, nil
  end
  local win = select(2, pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_peek_win"))
  local buf = select(2, pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_diff_buf"))
  if not (type(win) == "number" and vim.api.nvim_win_is_valid(win)
    and type(buf) == "number" and vim.api.nvim_buf_is_valid(buf)) then
    return nil, nil
  end
  if vim.api.nvim_win_get_buf(win) ~= buf then
    vim.api.nvim_win_set_buf(win, buf)
  end
  return win, buf
end

local function jump_hunk_mark(key)
  local win, buf = show_hunk_overview()
  if not win then
    return
  end
  local marks = (buf_meta[buf] or {}).hunk_at or {}
  local lnum = marks[key]
  if not lnum then
    for k, v in pairs(marks) do
      if k:sub(-#key) == key then
        lnum = v
        break
      end
    end
  end
  if not lnum then
    return
  end
  vim.api.nvim_win_set_cursor(win, { math.min(lnum, vim.api.nvim_buf_line_count(buf)), 0 })
  vim.api.nvim_win_call(win, function()
    vim.cmd("normal! zt")
  end)
end

function reveal_hunk(repo, rel, _)
  jump_hunk_mark(repo .. "/" .. (rel or ""))
end

local function show_incoming(repo)
  local tree
  for _, t in ipairs(worktrees()) do
    if t.name == repo then
      tree = t
      break
    end
  end
  if not tree then
    return
  end
  local tab = collection_tab()
  if not tab then
    return
  end
  local win = select(2, pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_peek_win"))
  if not (type(win) == "number" and vim.api.nvim_win_is_valid(win)) then
    return
  end
  local ref = select(1, compare_ref(tree.path, tree.name))
  local behind = count_revs(tree.path, "HEAD.." .. ref)
  local log = git(tree.path, { "log", "--oneline", "HEAD.." .. ref })
  local patch = git(tree.path, { "diff", "--no-ext-diff", "HEAD.." .. ref })
  local name = "wtc-diff://incoming"
  local buf = vim.fn.bufnr(name)
  if buf == -1 then
    buf = vim.api.nvim_create_buf(false, true)
    pcall(vim.api.nvim_buf_set_name, buf, name)
  end
  vim.bo[buf].buftype = "nofile"
  vim.bo[buf].bufhidden = "hide"
  vim.bo[buf].swapfile = false
  local lines = {
    string.format("# %s  incoming from %s  ↓%d", tree.name, ref, behind),
    "# this is catch-up — not our work since branch-off",
    "",
  }
  if log ~= "" then
    lines[#lines + 1] = "# commits"
    for line in (log .. "\n"):gmatch("([^\n]*)\n") do
      if line ~= "" then
        lines[#lines + 1] = "#   " .. line
      end
    end
    lines[#lines + 1] = ""
  end
  if patch ~= "" then
    for line in (patch .. "\n"):gmatch("([^\n]*)\n") do
      lines[#lines + 1] = line
    end
  elseif behind == 0 then
    lines[#lines + 1] = "# nothing incoming"
  end
  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.bo[buf].modifiable = false
  vim.bo[buf].filetype = "diff"
  vim.api.nvim_win_set_buf(win, buf)
  vim.api.nvim_win_set_cursor(win, { 1, 0 })
end

local function start_change_watch()
  if watch_timer then
    return
  end
  -- uv timers are userdata — never store them on vim.g / vim.t (E5101).
  watch_timer = vim.uv.new_timer()
  watch_timer:start(3000, 3000, vim.schedule_wrap(function()
    local ok, err = pcall(function()
      local found
      for _, tab in ipairs(vim.api.nvim_list_tabpages()) do
        local yes, kind = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_kind")
        if yes and kind == "diff-all" then
          found = tab
          break
        end
      end
      if not found then
        watch_timer:stop()
        watch_timer = nil
        return
      end
      refresh_collection_tab(found)
    end)
    if not ok then
      vim.notify("wtc-browse watch: " .. tostring(err), vim.log.levels.DEBUG)
    end
  end))
end

local function setup_collection_layout(index_buf)
  vim.api.nvim_tabpage_set_var(0, "wtc_follow", true)
  vim.api.nvim_tabpage_set_var(0, "wtc_follow_last", os.time()) -- don't jump to old files on boot
  local tree_buf = vim.api.nvim_create_buf(false, true)
  pcall(vim.api.nvim_buf_set_name, tree_buf, "wtc-diff://tree")
  vim.bo[tree_buf].buftype = "nofile"
  vim.bo[tree_buf].bufhidden = "hide"
  vim.bo[tree_buf].swapfile = false
  vim.bo[tree_buf].filetype = "wtctree"
  local snaps = snapshots_all()
  vim.api.nvim_tabpage_set_var(0, "wtc_fp", fingerprint(snaps))
  render_change_tree(tree_buf, snaps)

  local function activate_tree_node()
    local node = tree_node_at_cursor(tree_buf)
    if type(node) ~= "table" then
      return
    end
    if node.kind == "file" then
      reveal_hunk(node.repo, node.path, node.mb)
    elseif node.kind == "catchup" then
      show_incoming(node.repo)
    elseif node.kind == "repo" then
      local snaps = (buf_meta[tree_buf] or {}).snapshots or {}
      local behind, nfiles = 0, 0
      for _, s in ipairs(snaps) do
        if s.tree.name == node.repo then
          behind = s.behind or 0
          nfiles = #(s.files or {})
          break
        end
      end
      if nfiles == 0 and behind > 0 then
        show_incoming(node.repo)
      else
        jump_hunk_mark(node.repo)
      end
    end
  end
  vim.keymap.set("n", "<CR>", activate_tree_node, { buffer = tree_buf, silent = true, desc = "Show file diff" })
  vim.keymap.set("n", "<2-LeftMouse>", activate_tree_node, { buffer = tree_buf, silent = true, desc = "Show file diff" })
  vim.keymap.set("n", "o", function()
    local node = tree_node_at_cursor(tree_buf)
    if type(node) ~= "table" then
      return
    end
    if node.kind == "file" then
      open_changed_file(node.repo, node.path)
    elseif node.kind == "repo" or node.kind == "catchup" then
      _G.WtcBrowseFocus(node.repo, "diff")
    end
  end, { buffer = tree_buf, silent = true, desc = "Open in repo tab" })
  vim.keymap.set("n", "f", function()
    local on = rawget(vim.t, "wtc_follow")
    if on == nil then
      on = true
    end
    vim.t.wtc_follow = not on
    render_change_tree(tree_buf, (buf_meta[tree_buf] or {}).snapshots or snapshots_all())
  end, { buffer = tree_buf, silent = true, desc = "Toggle follow" })
  vim.keymap.set("n", "r", function()
    local s = snapshots_all()
    vim.t.wtc_fp = fingerprint(s)
    render_change_tree(tree_buf, s)
    fill_hunk_buf(index_buf)
    show_hunk_overview()
  end, { buffer = tree_buf, silent = true, desc = "Refresh tree" })

  -- Tree left, stacked hunks (all siblings) on the right.
  fill_hunk_buf(index_buf)
  vim.api.nvim_set_current_buf(tree_buf)
  vim.cmd("vertical rightbelow split")
  local peek_win = vim.api.nvim_get_current_win()
  vim.api.nvim_set_current_buf(index_buf)
  vim.cmd("wincmd h")
  local tree_win = vim.api.nvim_get_current_win()
  vim.api.nvim_win_set_width(tree_win, 42)
  vim.wo[tree_win].winfixwidth = true
  vim.wo[tree_win].number = false
  vim.wo[tree_win].relativenumber = false
  vim.wo[tree_win].signcolumn = "no"
  vim.wo[tree_win].statuscolumn = ""
  vim.wo[tree_win].wrap = false
  vim.t.wtc_tree_buf = tree_buf
  vim.t.wtc_tree_win = tree_win
  vim.t.wtc_peek_win = peek_win
  vim.t.wtc_diff_buf = index_buf
  start_change_watch()
end

local function close_dashboards()
  for _, w in ipairs(vim.api.nvim_list_wins()) do
    local ft = vim.bo[vim.api.nvim_win_get_buf(w)].filetype
    if ft == "dashboard" or ft == "alpha" or ft == "snacks_dashboard" or ft == "ministarter" then
      pcall(vim.api.nvim_win_close, w, true)
    end
  end
end

local function load_neotree()
  if package.loaded["neo-tree"] then
    return true
  end
  return pcall(require, "neo-tree")
end

local function show_tree(dir, source, keep_focus)
  source = source or "filesystem"
  if not dir or dir == "" then
    return false
  end
  if last_tree_dir == dir and last_tree_source == source then
    for _, w in ipairs(vim.api.nvim_tabpage_list_wins(0)) do
      if vim.bo[vim.api.nvim_win_get_buf(w)].filetype == "neo-tree" then
        return true
      end
    end
  end
  load_neotree()
  pcall(function()
    require("neo-tree.command").execute({ action = "close" })
  end)
  last_tree_dir, last_tree_source = dir, source
  local action = keep_focus and "show" or "focus"
  local ok = pcall(function()
    require("neo-tree.command").execute({
      action = action,
      source = source,
      position = "left",
      dir = dir,
    })
  end)
  if ok then
    return true
  end
  if vim.fn.exists(":Neotree") == 2 then
    return pcall(vim.cmd, string.format(
      "Neotree action=%s source=%s position=left dir=%s",
      action,
      source,
      vim.fn.fnameescape(dir)
    ))
  end
  return false
end

local function current_repo_path()
  local ok, name = pcall(vim.api.nvim_tabpage_get_var, 0, "wtc_repo")
  if not ok or not name then
    return nil
  end
  return root .. "/" .. name, name
end

local function sibling_dirs()
  local dirs = {}
  for _, t in ipairs(worktrees()) do
    dirs[#dirs + 1] = t.path
  end
  return dirs
end

local function repo_for_path(path)
  path = vim.fs.normalize(vim.fn.fnamemodify(path, ":p"))
  for _, t in ipairs(worktrees()) do
    local prefix = t.path .. "/"
    if path == t.path or path:sub(1, #prefix) == prefix then
      return t.name, path:sub(#t.path + 2)
    end
  end
  return nil
end

local function apply_tabline()
  -- Native clickable tabs. bufferline.setup() here previously ate the
  -- tabline (and VeryLazy then hid it). %NT is stock vim (:help setting-tabline).
  vim.o.showtabline = 2
  vim.o.tabline = "%!v:lua.WtcBrowseTabline()"
  if tabline_hl_done then
    return
  end
  tabline_hl_done = true
  -- Tokyonight sets TabLine to fg_gutter on bg_statusline — meant as a
  -- leftover under bufferline, not as a project switcher. Use Normal fg.
  local normal = vim.api.nvim_get_hl(0, { name = "Normal", link = false })
  local fill = vim.api.nvim_get_hl(0, { name = "TabLineFill", link = false })
  vim.api.nvim_set_hl(0, "TabLine", { fg = normal.fg, bg = fill.bg })
end

function _G.WtcBrowseTabline()
  local parts = {}
  local cur = vim.api.nvim_get_current_tabpage()
  for i, tab in ipairs(vim.api.nvim_list_tabpages()) do
    local ok_kind, kind = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_kind")
    local ok_repo, repo = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_repo")
    local name = (ok_kind and kind == "diff-all" and "diff")
      or (ok_repo and repo)
      or tostring(i)
    local hl = tab == cur and "%#TabLineSel#" or "%#TabLine#"
    parts[#parts + 1] = string.format("%%%dT%s %s %%T", i, hl, name)
  end
  parts[#parts + 1] = "%#TabLineFill#%T"
  return table.concat(parts)
end

local function find_tab(repo)
  for _, tab in ipairs(vim.api.nvim_list_tabpages()) do
    local ok, name = pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_repo")
    if ok and name == repo then
      return tab
    end
  end
  return nil
end

local function ensure_unified()
  local ok, unified = pcall(require, "unified")
  if not ok then
    return false
  end
  pcall(unified.setup, {})
  return true
end

function apply_inline_diff(mb, buf, win)
  if not mb or mb == "" then
    return
  end
  buf = buf or vim.api.nvim_get_current_buf()
  if not vim.api.nvim_buf_is_valid(buf) then
    return
  end
  if ensure_unified() then
    local ok = pcall(function()
      require("unified.diff").show(mb, buf)
    end)
    if ok then
      if win and vim.api.nvim_win_is_valid(win) then
        pcall(function()
          require("unified.navigation").jump_to_first_hunk(win, buf)
        end)
      end
      return
    end
  end
  vim.schedule(function()
    pcall(function()
      require("gitsigns").change_base(mb, false)
    end)
  end)
end

function open_changed_file(repo, rel)
  local tab = find_tab(repo)
  if not tab then
    return
  end
  vim.api.nvim_set_current_tabpage(tab)
  local path = root .. "/" .. repo .. "/" .. rel
  vim.cmd.tcd(vim.fn.fnameescape(root .. "/" .. repo))
  if vim.uv.fs_stat(path) then
    load_file_in_win(vim.api.nvim_get_current_win(), path)
  end
  local mb = rawget(vim.t, "wtc_base")
  apply_inline_diff(mb, vim.api.nvim_get_current_buf())
  show_tree(root .. "/" .. repo, "filesystem", true)
end

function _G.WtcBrowseFocus(repo, want)
  if type(repo) ~= "string" or repo == "" then
    return "bad-repo"
  end
  local tab = find_tab(repo)
  if not tab then
    return "missing"
  end
  vim.api.nvim_set_current_tabpage(tab)
  local path = root .. "/" .. repo
  vim.cmd.tcd(vim.fn.fnameescape(path))
  want = want or "files"
  if want == "git" then
    show_tree(path, "git_status")
  elseif want == "diff" then
    local dbuf = rawget(vim.t, "wtc_diff_buf")
    if dbuf and vim.api.nvim_buf_is_valid(dbuf) then
      vim.api.nvim_set_current_buf(dbuf)
    end
    show_tree(path, "filesystem", true)
  elseif want == "files" or want == "" or want == vim.NIL then
    show_tree(path, "filesystem")
  else
    open_changed_file(repo, want)
  end
  return "ok"
end

function _G.WtcBrowsePr(repo, number)
  local focused = _G.WtcBrowseFocus(repo, "files")
  if focused ~= "ok" then
    return focused
  end
  if vim.fn.exists(":Octo") ~= 2 then
    return "no-octo"
  end
  number = tostring(number or "")
  if number == "" or number == "vim.NIL" then
    vim.cmd("Octo pr list")
    return "octo-list"
  end
  vim.cmd("Octo pr edit " .. number)
  return "octo"
end

local ns = vim.api.nvim_create_namespace("wtc_index")

local function render_index(buf, scope)
  -- scope: nil = all repos, or a repo name
  local snapshots = {}
  for _, t in ipairs(worktrees()) do
    if not scope or t.name == scope then
      snapshots[#snapshots + 1] = collect_changes(t)
    end
  end

  local lines = {}
  local meta = {} -- 1-based line → { repo, path } or { repo }
  local function add(text, info)
    lines[#lines + 1] = text
    if info then
      meta[#lines] = info
    end
  end

  if not scope then
    add(vim.fs.basename(root) .. "   changes since branch-off")
    add("<CR> open file   r refresh   ]f/[f next file   <leader>gd index   <leader>gD side-by-side")
    add("")
  end

  for _, snap in ipairs(snapshots) do
    local n = #snap.files
    local bits = {
      string.format("%-22s  since %s", snap.tree.name, (snap.mb or "?"):sub(1, 7)),
    }
    if (snap.ahead or 0) > 0 then
      bits[#bits + 1] = "↑" .. snap.ahead
    end
    if n == 0 then
      bits[#bits + 1] = "clean"
    else
      bits[#bits + 1] = n .. (n == 1 and " file" or " files")
    end
    if (snap.behind or 0) > 0 then
      bits[#bits + 1] = "↓" .. snap.behind .. " " .. snap.ref
    end
    add(table.concat(bits, "  "), { repo = snap.tree.name })
    if (snap.behind or 0) > 0 then
      local noun = snap.behind == 1 and "commit" or "commits"
      add(string.format("    ↓ catch-up  %s is %d %s ahead", snap.ref, snap.behind, noun))
    end
    if n == 0 then
      add("    ·  no changes since branch-off")
    else
      for _, f in ipairs(snap.files) do
        local counts
        if f.kind == "untracked" then
          counts = "  ?    "
        elseif f.kind == "dirty" then
          counts = string.format("*+%-4d −%-4d", f.add, f.del)
        else
          counts = string.format(" +%-4d −%-4d", f.add, f.del)
        end
        add(string.format("    %s  %s", counts, f.path), {
          repo = snap.tree.name,
          path = f.path,
        })
      end
    end
    add("")
  end

  vim.bo[buf].modifiable = true
  vim.api.nvim_buf_set_lines(buf, 0, -1, false, lines)
  vim.api.nvim_buf_clear_namespace(buf, ns, 0, -1)
  for i, line in ipairs(lines) do
    if line:match("^%S") and not line:match("^<") then
      pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, 0, {
        end_col = #line,
        hl_group = "Title",
      })
    elseif line:match("catch%-up") then
      pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, 0, {
        end_col = #line,
        hl_group = "DiagnosticWarn",
      })
    elseif line:match("%?    ") then
      pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, 4, {
        end_col = 8,
        hl_group = "DiagnosticHint",
      })
    elseif line:match("^%s+%*") then
      local star = line:find("*", 1, true)
      if star then
        pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, star - 1, {
          end_col = star,
          hl_group = "DiagnosticWarn",
        })
      end
      local plus = line:find("+", 1, true)
      local minus = line:find("−", 1, true) or line:find("-", 1, true)
      if plus then
        pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, plus - 1, {
          end_col = plus + 4,
          hl_group = "DiffAdd",
        })
      end
      if minus then
        pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, minus - 1, {
          end_col = minus + 5,
          hl_group = "DiffDelete",
        })
      end
    elseif line:match("^%s+%+") then
      local plus = line:find("+", 1, true)
      local minus = line:find("−", 1, true) or line:find("-", 1, true)
      if plus then
        pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, plus - 1, {
          end_col = plus + 4,
          hl_group = "DiffAdd",
        })
      end
      if minus then
        pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, minus - 1, {
          end_col = minus + 5,
          hl_group = "DiffDelete",
        })
      end
    elseif line:match("no changes") then
      pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, 0, {
        end_col = #line,
        hl_group = "Comment",
      })
    elseif line:match("^<") then
      pcall(vim.api.nvim_buf_set_extmark, buf, ns, i - 1, 0, {
        end_col = #line,
        hl_group = "Comment",
      })
    end
  end
  vim.bo[buf].modifiable = false
  buf_meta[buf] = buf_meta[buf] or {}
  buf_meta[buf].index = meta
  buf_meta[buf].snapshots = snapshots
end

local function jump_from_index()
  local lnum = vim.api.nvim_win_get_cursor(0)[1]
  local meta = (buf_meta[vim.api.nvim_get_current_buf()] or {}).index or {}
  local info = meta[lnum] or meta[tostring(lnum)]
  if type(info) ~= "table" or not info.repo then
    return
  end
  if info.path then
    open_changed_file(info.repo, info.path)
    return
  end
  _G.WtcBrowseFocus(info.repo, "diff")
end

local function files_from_snaps(snaps)
  local out = {}
  if type(snaps) ~= "table" then
    return out
  end
  for _, snap in ipairs(snaps) do
    for _, f in ipairs(snap.files or {}) do
      out[#out + 1] = { repo = snap.tree.name, path = f.path }
    end
  end
  return out
end

local function changed_files_on_tab()
  local cur = buf_meta[vim.api.nvim_get_current_buf()]
  local files = files_from_snaps(cur and cur.snapshots)
  if #files > 0 then
    return files
  end
  local dbuf = rawget(vim.t, "wtc_diff_buf")
  if dbuf and vim.api.nvim_buf_is_valid(dbuf) then
    return files_from_snaps((buf_meta[dbuf] or {}).snapshots)
  end
  return {}
end

local function step_changed(delta)
  local files = changed_files_on_tab()
  if #files == 0 then
    return
  end
  local cur = vim.api.nvim_buf_get_name(0)
  local idx = 0
  for i, f in ipairs(files) do
    if cur:sub(-#f.path) == f.path then
      idx = i
      break
    end
  end
  idx = idx + delta
  if idx < 1 then
    idx = #files
  elseif idx > #files then
    idx = 1
  end
  open_changed_file(files[idx].repo, files[idx].path)
end

local function ensure_index_buf(label, repo)
  local name = "wtc-diff://" .. label
  local buf = vim.fn.bufnr(name)
  if buf == -1 then
    buf = vim.api.nvim_create_buf(true, true)
    pcall(vim.api.nvim_buf_set_name, buf, name)
  end
  vim.bo[buf].buftype = "nofile"
  vim.bo[buf].bufhidden = "hide"
  vim.bo[buf].swapfile = false
  vim.bo[buf].filetype = "wtcindex"
  vim.b[buf].wtc_diff = true
  -- repo name only — keep tables off vim.b
  if repo then
    vim.b[buf].wtc_diff_repo = repo
  end
  vim.keymap.set("n", "<CR>", jump_from_index, { buffer = buf, silent = true, desc = "Open change" })
  vim.keymap.set("n", "r", function()
    render_index(buf, repo)
  end, { buffer = buf, silent = true, desc = "Refresh index" })
  vim.keymap.set("n", "]f", function()
    step_changed(1)
  end, { buffer = buf, silent = true, desc = "Next changed file" })
  vim.keymap.set("n", "[f", function()
    step_changed(-1)
  end, { buffer = buf, silent = true, desc = "Prev changed file" })
  render_index(buf, repo)
  return buf
end

local function open_from_picker(path, lnum, col)
  if not path or path == "" then
    return
  end
  local repo, rel = repo_for_path(path)
  if repo then
    _G.WtcBrowseFocus(repo, (rel and rel ~= "") and rel or "files")
  else
    vim.cmd.edit(vim.fn.fnameescape(path))
  end
  if type(lnum) == "number" and lnum > 0 then
    pcall(vim.api.nvim_win_set_cursor, 0, { lnum, math.max((col or 1) - 1, 0) })
  end
end

local function pick_all(builtin, title)
  local dirs = sibling_dirs()
  if #dirs == 0 then
    return
  end
  require("telescope.builtin")[builtin]({
    prompt_title = title,
    search_dirs = dirs,
    attach_mappings = function()
      local actions = require("telescope.actions")
      local state = require("telescope.actions.state")
      actions.select_default:replace(function(prompt_bufnr)
        local entry = state.get_selected_entry()
        actions.close(prompt_bufnr)
        if not entry then
          return
        end
        open_from_picker(entry.path or entry.filename, entry.lnum, entry.col)
      end)
      return true
    end,
  })
end

local function map_search()
  local function files()
    pick_all("find_files", "Files (collection)")
  end
  local function grep()
    pick_all("live_grep", "Grep (collection)")
  end
  vim.keymap.set("n", "<leader><space>", files, { desc = "Find Files (collection)" })
  vim.keymap.set("n", "<leader>ff", files, { desc = "Find Files (collection)" })
  vim.keymap.set("n", "<leader>/", grep, { desc = "Grep (collection)" })
  vim.keymap.set("n", "<leader>sg", grep, { desc = "Grep (collection)" })
  vim.keymap.set("n", "<leader>fp", function()
    vim.ui.select(worktrees(), {
      prompt = "Repo",
      format_item = function(t)
        return t.name
      end,
    }, function(t)
      if t then
        _G.WtcBrowseFocus(t.name, "files")
      end
    end)
  end, { desc = "Switch repo tab" })
  vim.keymap.set("n", "<leader>gd", function()
    local dbuf = rawget(vim.t, "wtc_diff_buf")
    if dbuf and vim.api.nvim_buf_is_valid(dbuf) then
      vim.api.nvim_set_current_buf(dbuf)
    end
  end, { desc = "Back to change index" })
  vim.keymap.set("n", "]f", function()
    step_changed(1)
  end, { desc = "Next changed file" })
  vim.keymap.set("n", "[f", function()
    step_changed(-1)
  end, { desc = "Prev changed file" })
  vim.keymap.set("n", "]h", function()
    pcall(function()
      require("unified.navigation").next_hunk()
    end)
  end, { desc = "Next hunk" })
  vim.keymap.set("n", "[h", function()
    pcall(function()
      require("unified.navigation").previous_hunk()
    end)
  end, { desc = "Prev hunk" })
  vim.keymap.set("n", "<leader>gD", function()
    local path = current_repo_path()
    local base = rawget(vim.t, "wtc_base")
    if not path then
      vim.notify("diffview is per-repo — open a sibling tab first", vim.log.levels.INFO)
      return
    end
    if vim.fn.exists(":DiffviewOpen") ~= 2 then
      vim.notify("install sindrets/diffview.nvim for side-by-side", vim.log.levels.WARN)
      return
    end
    vim.cmd("DiffviewOpen " .. (base or "HEAD") .. " -- " .. vim.fn.fnameescape(path))
  end, { desc = "Side-by-side vs branch root" })
end

local function has_browse_tabs()
  for _, tab in ipairs(vim.api.nvim_list_tabpages()) do
    if pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_repo")
      or pcall(vim.api.nvim_tabpage_get_var, tab, "wtc_kind")
    then
      return true
    end
  end
  return false
end

local function start()
  pcall(function()
    require("persistence").stop()
  end)
  pcall(function()
    require("snacks.indent").disable()
  end)
  close_dashboards()
  load_neotree()

  if not has_browse_tabs() then
    local trees = worktrees()
    if #trees == 0 then
      vim.notify("wtc-browse: no git worktrees under " .. root, vim.log.levels.WARN)
      return
    end

    vim.cmd.enew()
    vim.api.nvim_tabpage_set_var(0, "wtc_kind", "diff-all")
    local all_buf = ensure_index_buf("collection", nil)
    vim.api.nvim_set_current_buf(all_buf)
    vim.t.wtc_diff_buf = all_buf
    setup_collection_layout(all_buf)

    for _, t in ipairs(trees) do
      vim.cmd.tabnew()
      vim.cmd.tcd(vim.fn.fnameescape(t.path))
      vim.api.nvim_tabpage_set_var(0, "wtc_repo", t.name)
      local snap = collect_changes(t)
      vim.t.wtc_base = snap.mb
      local dbuf = ensure_index_buf(t.name, t.name)
      vim.t.wtc_diff_buf = dbuf
      local first
      for _, f in ipairs(snap.files) do
        if f.kind == "dirty" or f.kind == "untracked" then
          first = f
          break
        end
      end
      first = first or snap.files[1]
      if first and ensure_unified() then
        local path = t.path .. "/" .. first.path
        if vim.uv.fs_stat(path) then
          load_file_in_win(vim.api.nvim_get_current_win(), path)
          apply_inline_diff(snap.mb, vim.api.nvim_get_current_buf())
        else
          vim.api.nvim_set_current_buf(dbuf)
        end
      else
        vim.api.nvim_set_current_buf(dbuf)
      end
    end
    vim.cmd.tabfirst()
    pcall(function()
      require("neo-tree.command").execute({ action = "close" })
    end)
    local tw = rawget(vim.t, "wtc_tree_win")
    if tw and vim.api.nvim_win_is_valid(tw) then
      vim.api.nvim_set_current_win(tw)
    end
  end

  vim.opt.mouse = "a"
  apply_tabline()
  map_search()
  vim.keymap.set("n", "gt", "<cmd>tabnext<cr>", { silent = true, desc = "Next repo tab" })
  vim.keymap.set("n", "gT", "<cmd>tabprevious<cr>", { silent = true, desc = "Prev repo tab" })

  vim.api.nvim_create_autocmd("FileType", {
    group = vim.api.nvim_create_augroup("wtc_browse_neotree", { clear = true }),
    pattern = "neo-tree",
    callback = function()
      vim.opt_local.number = false
      vim.opt_local.relativenumber = false
      vim.opt_local.signcolumn = "no"
      vim.opt_local.statuscolumn = ""
    end,
  })

  vim.api.nvim_create_autocmd({ "TabEnter", "UIEnter" }, {
    group = vim.api.nvim_create_augroup("wtc_browse_tabs", { clear = true }),
    callback = function()
      local ok_kind, kind = pcall(vim.api.nvim_tabpage_get_var, 0, "wtc_kind")
      if ok_kind and kind == "diff-all" then
        pcall(function()
          require("neo-tree.command").execute({ action = "close" })
        end)
        apply_tabline()
        return
      end
      apply_tabline()
      if vim.b.wtc_diff then
        show_tree(select(1, current_repo_path()), "filesystem", true)
        return
      end
      local name = vim.api.nvim_buf_get_name(0)
      local is_file = vim.bo.buftype == "" and name ~= "" and vim.uv.fs_stat(name)
      local path = current_repo_path()
      if path then
        show_tree(path, "filesystem", is_file or false)
        if is_file then
          apply_inline_diff(rawget(vim.t, "wtc_base"), vim.api.nvim_get_current_buf())
        end
      end
    end,
  })
end

local function start_once()
  if started then
    return
  end
  started = true
  local ok, err = pcall(start)
  if not ok then
    vim.notify("wtc-browse: " .. tostring(err), vim.log.levels.ERROR)
  end
end

vim.api.nvim_create_autocmd("User", {
  pattern = "VeryLazy",
  once = true,
  callback = start_once,
})
vim.defer_fn(start_once, 1200)
