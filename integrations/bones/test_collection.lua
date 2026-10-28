-- Run with: lua integrations/bones/test_collection.lua /path/to/patched/functions.lua
-- Exercises the supplied bones callbacks with a small in-memory Luanti mock.
local source = assert(arg[1], "expected path to patched bones/functions.lua")
local logs, removed, waypoint_removed, items, room, owner, infotext
local pos = {x = 2, y = -3, z = 4}
local death_pos = {x = 1, y = -3, z = 4}
local on_dieplayer, placed

local grave_inv = {
	is_empty = function() return items == 0 end,
	get_size = function() return 1 end,
	get_stack = function() return "item" end,
	set_stack = function() items = 0 end,
	set_size = function() end,
	room_for_item = function() return true end,
	add_item = function() items = items + 1 end,
}
local player_inv = {
	is_empty = function() return false end,
	get_size = function() return 1 end,
	get_stack = function() return "item" end,
	set_list = function() end,
	room_for_item = function() return room end,
	add_item = function() end,
}
local player = {
	get_player_name = function() return "Collector" end,
	get_inventory = function() return player_inv end,
	get_pos = function() return death_pos end,
	get_look_dir = function() return {x = 0, y = 0, z = 1} end,
}
local meta = {
	get_inventory = function() return grave_inv end,
	get_string = function(_, key)
		if key == "owner" then return owner end
		if key == "infotext" then return infotext end
		return ""
	end,
	set_string = function() end,
	set_int = function() end,
}
core = {
	settings = {get = function() return nil end, get_bool = function() return true end},
	get_translator = function() return function(s) return s end end,
	get_current_modname = function() return "bones" end,
	get_meta = function() return meta end,
	check_player_privs = function() return false end,
	get_modpath = function() return "mock" end,
	remove_node = function(p) assert(p == pos); removed = removed + 1 end,
	log = function(level, text)
		assert(level == "action")
		logs[#logs + 1] = text
	end,
	pos_to_string = function(p) return string.format("(%d,%d,%d)", p.x, p.y, p.z) end,
	register_on_dieplayer = function(fn) on_dieplayer = fn end,
	is_creative_enabled = function() return false end,
	get_node = function() return {name = "stone"} end,
	registered_nodes = {stone = {liquidtype = "none", buildable_to = false}},
	is_protected = function() return false end,
	find_node_near = function() return pos end,
	dir_to_facedir = function() return 0 end,
	set_node = function(p) placed = p end,
	chat_send_player = function() end,
	get_node_timer = function() return {start = function() end} end,
}
named_waypoints = {
	remove_waypoint = function(kind, p)
		assert(kind == "bones" and p == pos)
		waypoint_removed = waypoint_removed + 1
	end,
	add_waypoint = function() end,
}
vector = {round = function(p) return p end}
mcl_formspec = {get_itemslot_bg = function() return "" end}
bones = {player_inventory_lists = {"main"}}
dofile(source)

local function reset()
	logs, removed, waypoint_removed, items, room = {}, 0, 0, 1, true
	owner, infotext = "Collector", "Fresh bones"
end
local function expect_collection()
	assert(items == 0 and removed == 1 and waypoint_removed == 1)
	assert(#logs == 1 and logs[1] == "Collector collects bones:bones at (2,-3,4)")
end
local function expect_no_collection()
	assert(#logs == 0 and removed == 0 and waypoint_removed == 0)
end

reset()
bones.on_punch(pos, {}, player)
expect_collection()

reset()
room = false
bones.on_punch(pos, {}, player)
expect_no_collection()
assert(items == 1)

reset()
owner = "SomeoneElse"
bones.on_punch(pos, {}, player)
expect_no_collection()
assert(items == 1)

reset()
owner = "" -- Public grave after the sharing timer expires.
bones.on_punch(pos, {}, player)
expect_collection()

reset()
bones.on_metadata_inventory_take(pos, "main", 1, {}, player)
expect_no_collection() -- Taking only some items must leave the grave active.
items = 0
bones.on_metadata_inventory_take(pos, "main", 1, {}, player)
expect_collection()

reset()
on_dieplayer(player)
assert(placed == pos and placed ~= death_pos)
assert(#logs == 1 and logs[1] == "Collector dies at (2,-3,4). Bones placed")
print("PASS: punch, full inventory, ownership, public grave, partial/final take, relocated grave")
