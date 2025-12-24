-- FlatRun Security Event Capture
-- This script captures security-relevant events and sends them to the agent API

local cjson = require "cjson.safe"
local http = require "resty.http"

local _M = {}

-- Configuration (injected by agent during deployment)
local AGENT_IP = "{{.AgentIP}}"
local AGENT_PORT = {{.AgentPort}}
local INTERNAL_TOKEN = "{{.InternalAPIToken}}"

-- Cache settings
local CACHE_TTL = 30  -- seconds
local BLOCKED_IPS_LAST_FETCH = "blocked_ips_last_fetch"
local WHITELIST_LAST_FETCH = "whitelist_last_fetch"

-- Suspicious paths patterns
local suspicious_patterns = {
    "%.env",
    "%.git",
    "wp%-admin",
    "wp%-login",
    "wp%-config",
    "xmlrpc%.php",
    "phpmyadmin",
    "adminer",
    "/admin",
    "/administrator",
    "/shell",
    "/cmd",
    "/backdoor",
    "%.sql",
    "%.bak",
    "%.backup",
    "%.old",
    "/actuator",
    "/swagger",
    "/api%-docs",
    "%.aws",
    "%.ssh",
    "%.docker",
    "/debug",
    "/trace",
    "composer%.json",
    "package%.json",
}

-- Scanner user agent patterns
local scanner_patterns = {
    "nikto",
    "nmap",
    "sqlmap",
    "dirbuster",
    "gobuster",
    "nuclei",
    "masscan",
    "wpscan",
    "burp",
    "acunetix",
    "nessus",
    "zgrab",
}

local function get_real_client_ip()
    local cf_ip = ngx.var.http_cf_connecting_ip
    if cf_ip and cf_ip ~= "" then
        return cf_ip
    end

    local xff = ngx.var.http_x_forwarded_for
    if xff and xff ~= "" then
        local first_ip = xff:match("^([^,]+)")
        if first_ip then
            return first_ip:match("^%s*(.-)%s*$")
        end
    end

    return ngx.var.remote_addr
end

function _M.get_client_ip()
    return get_real_client_ip()
end

function _M.is_blocked(ip)
    if not ip then return false end
    if _M.is_whitelisted(ip, nil) then return false end

    local dict = ngx.shared.blocked_ips
    if not dict then return false end

    local is_blocked = dict:get("ip:" .. ip)
    if is_blocked ~= nil then
        return is_blocked
    end

    -- Check if we need to refresh the cache
    local last_fetch = dict:get(BLOCKED_IPS_LAST_FETCH) or 0
    local now = ngx.time()

    if now - last_fetch > CACHE_TTL then
        -- Refresh in background to not block the request
        ngx.timer.at(0, function()
            _M.refresh_blocked_ips()
        end)
    end

    return false
end

-- Fetch blocked IPs from agent API and cache them
function _M.refresh_blocked_ips()
    local dict = ngx.shared.blocked_ips
    if not dict then return end

    local httpc = http.new()
    httpc:set_timeout(3000)

    local conn_ok, conn_err = httpc:connect({
        host = AGENT_IP,
        port = AGENT_PORT,
        scheme = "http",
    })

    if not conn_ok then
        ngx.log(ngx.ERR, "Failed to connect to agent for blocked IPs: ", conn_err)
        return
    end

    local res, req_err = httpc:request({
        method = "GET",
        path = "/api/_internal/blocked-ips",
        headers = {
            ["Host"] = AGENT_IP .. ":" .. AGENT_PORT,
            ["X-Internal-Token"] = INTERNAL_TOKEN,
        }
    })

    if not res then
        ngx.log(ngx.ERR, "Failed to fetch blocked IPs: ", req_err)
        httpc:close()
        return
    end

    local body = res:read_body()
    httpc:close()

    if res.status ~= 200 then
        ngx.log(ngx.ERR, "Blocked IPs API returned status: ", res.status)
        return
    end

    local data, decode_err = cjson.decode(body)
    if not data then
        ngx.log(ngx.ERR, "Failed to decode blocked IPs response: ", decode_err)
        return
    end

    -- Clear old entries and set new ones
    dict:flush_all()
    dict:set(BLOCKED_IPS_LAST_FETCH, ngx.time())

    local blocked_ips = data.blocked_ips or {}
    for _, entry in ipairs(blocked_ips) do
        if entry.ip then
            dict:set("ip:" .. entry.ip, true, CACHE_TTL * 2)
        end
    end

    ngx.log(ngx.INFO, "Refreshed blocked IPs cache: ", #blocked_ips, " IPs")
end

function _M.init_blocked_ips()
    ngx.timer.at(0, function()
        _M.refresh_blocked_ips()
    end)
end

local function is_ipv6(ip)
    return ip:find(":") ~= nil
end

local function ipv4_to_int(ip_str)
    local parts = {ip_str:match("^(%d+)%.(%d+)%.(%d+)%.(%d+)$")}
    if #parts ~= 4 then return nil end
    return tonumber(parts[1]) * 16777216 + tonumber(parts[2]) * 65536 +
           tonumber(parts[3]) * 256 + tonumber(parts[4])
end

local function expand_ipv6(ip)
    if ip:find("::") then
        local left, right = ip:match("^(.-)::(.*)$")
        left = left or ""
        right = right or ""
        local left_parts = {}
        local right_parts = {}
        for part in left:gmatch("[^:]+") do
            left_parts[#left_parts + 1] = part
        end
        for part in right:gmatch("[^:]+") do
            right_parts[#right_parts + 1] = part
        end
        local missing = 8 - #left_parts - #right_parts
        local parts = {}
        for _, p in ipairs(left_parts) do parts[#parts + 1] = p end
        for _ = 1, missing do parts[#parts + 1] = "0" end
        for _, p in ipairs(right_parts) do parts[#parts + 1] = p end
        return parts
    else
        local parts = {}
        for part in ip:gmatch("[^:]+") do
            parts[#parts + 1] = part
        end
        return parts
    end
end

local function ipv6_parts_to_ints(parts)
    if #parts ~= 8 then return nil end
    local ints = {}
    for i, p in ipairs(parts) do
        ints[i] = tonumber(p, 16) or 0
    end
    return ints
end

local function ipv6_match_cidr(ip_ints, cidr_ints, bits)
    local full_groups = math.floor(bits / 16)
    local remaining_bits = bits % 16

    for i = 1, full_groups do
        if ip_ints[i] ~= cidr_ints[i] then return false end
    end

    if remaining_bits > 0 and full_groups < 8 then
        local mask = bit.lshift(0xFFFF, 16 - remaining_bits)
        mask = bit.band(mask, 0xFFFF)
        if bit.band(ip_ints[full_groups + 1], mask) ~= bit.band(cidr_ints[full_groups + 1], mask) then
            return false
        end
    end

    return true
end

local function is_ip_in_cidr(ip, cidr)
    local cidr_ip, cidr_bits = cidr:match("^(.+)/(%d+)$")
    if not cidr_ip then return ip == cidr end

    local bits = tonumber(cidr_bits)
    local ip_is_v6 = is_ipv6(ip)
    local cidr_is_v6 = is_ipv6(cidr_ip)

    if ip_is_v6 ~= cidr_is_v6 then return false end

    if ip_is_v6 then
        local ip_parts = expand_ipv6(ip)
        local cidr_parts = expand_ipv6(cidr_ip)
        local ip_ints = ipv6_parts_to_ints(ip_parts)
        local cidr_ints = ipv6_parts_to_ints(cidr_parts)
        if not ip_ints or not cidr_ints then return false end
        return ipv6_match_cidr(ip_ints, cidr_ints, bits)
    else
        local ip_int = ipv4_to_int(ip)
        local cidr_int = ipv4_to_int(cidr_ip)
        if not ip_int or not cidr_int then return false end
        local mask = bits == 0 and 0 or (0xFFFFFFFF - (2^(32 - bits) - 1))
        return bit.band(ip_int, mask) == bit.band(cidr_int, mask)
    end
end

function _M.is_whitelisted(ip, path)
    local dict = ngx.shared.whitelist
    if not dict then return false end

    if ip then
        if dict:get("ip:" .. ip) then return true end
        local cidrs = dict:get("cidrs")
        if cidrs then
            for cidr in cidrs:gmatch("[^,]+") do
                if is_ip_in_cidr(ip, cidr) then return true end
            end
        end
    end

    if path then
        local paths = dict:get("paths")
        if paths then
            for wpath in paths:gmatch("[^,]+") do
                if path:sub(1, #wpath) == wpath then return true end
            end
        end
    end

    local last_fetch = dict:get(WHITELIST_LAST_FETCH) or 0
    if ngx.time() - last_fetch > CACHE_TTL then
        ngx.timer.at(0, function() _M.refresh_whitelist() end)
    end

    return false
end

function _M.refresh_whitelist()
    local dict = ngx.shared.whitelist
    if not dict then return end

    local httpc = http.new()
    httpc:set_timeout(3000)

    local conn_ok, conn_err = httpc:connect({
        host = AGENT_IP,
        port = AGENT_PORT,
        scheme = "http",
    })

    if not conn_ok then
        ngx.log(ngx.ERR, "Failed to connect to agent for whitelist: ", conn_err)
        return
    end

    local res, req_err = httpc:request({
        method = "GET",
        path = "/api/_internal/whitelist",
        headers = {
            ["Host"] = AGENT_IP .. ":" .. AGENT_PORT,
            ["X-Internal-Token"] = INTERNAL_TOKEN,
        }
    })

    if not res then
        ngx.log(ngx.ERR, "Failed to fetch whitelist: ", req_err)
        httpc:close()
        return
    end

    local body = res:read_body()
    httpc:close()

    if res.status ~= 200 then
        ngx.log(ngx.ERR, "Whitelist API returned status: ", res.status)
        return
    end

    local data, decode_err = cjson.decode(body)
    if not data then
        ngx.log(ngx.ERR, "Failed to decode whitelist response: ", decode_err)
        return
    end

    dict:flush_all()
    dict:set(WHITELIST_LAST_FETCH, ngx.time())

    local ips, cidrs, paths = {}, {}, {}
    for _, entry in ipairs(data.whitelist or {}) do
        if entry.type == "ip" then
            dict:set("ip:" .. entry.value, true, CACHE_TTL * 2)
            table.insert(ips, entry.value)
        elseif entry.type == "cidr" then
            table.insert(cidrs, entry.value)
        elseif entry.type == "path" then
            table.insert(paths, entry.value)
        end
    end

    if #cidrs > 0 then dict:set("cidrs", table.concat(cidrs, ","), CACHE_TTL * 2) end
    if #paths > 0 then dict:set("paths", table.concat(paths, ","), CACHE_TTL * 2) end

    ngx.log(ngx.INFO, "Refreshed whitelist: ", #ips, " IPs, ", #cidrs, " CIDRs, ", #paths, " paths")
end

function _M.init_whitelist()
    ngx.timer.at(0, function() _M.refresh_whitelist() end)
end

function _M.is_suspicious_path(uri)
    if not uri then return false end
    local uri_lower = string.lower(uri)
    for _, pattern in ipairs(suspicious_patterns) do
        if string.find(uri_lower, pattern) then
            return true
        end
    end
    return false
end

function _M.is_scanner(user_agent)
    if not user_agent then return false end
    local ua_lower = string.lower(user_agent)
    for _, pattern in ipairs(scanner_patterns) do
        if string.find(ua_lower, pattern) then
            return true
        end
    end
    return false
end

function _M.capture_event()
    local status = ngx.status
    local uri = ngx.var.uri
    local ip = get_real_client_ip()
    local method = ngx.var.request_method
    local user_agent = ngx.var.http_user_agent or ""
    local host = ngx.var.host or ""

    if _M.is_whitelisted(ip, uri) then return end

    local should_capture = false

    -- Scanner detection
    if _M.is_scanner(user_agent) then
        should_capture = true
    -- Rate limit hit
    elseif status == 429 then
        should_capture = true
    -- Auth failures
    elseif status == 401 or status == 403 then
        should_capture = true
    -- Server errors
    elseif status >= 500 then
        should_capture = true
    -- 404 on suspicious paths
    elseif status == 404 and _M.is_suspicious_path(uri) then
        should_capture = true
    -- Any non-200 on suspicious paths
    elseif _M.is_suspicious_path(uri) and status ~= 200 then
        should_capture = true
    end

    if not should_capture then
        return
    end

    -- Extract deployment name from host (remove port if present)
    local deployment_name = host:match("^([^:]+)")

    -- Send event to agent API (non-blocking)
    local ok, err = ngx.timer.at(0, function(premature)
        if premature then return end

        local body, encode_err = cjson.encode({
            source_ip = ip,
            request_path = uri,
            request_method = method,
            status_code = status,
            user_agent = user_agent,
            deployment_name = deployment_name,
            timestamp = ngx.time()
        })

        if not body then
            ngx.log(ngx.ERR, "Failed to encode security event: ", encode_err)
            return
        end

        local httpc = http.new()
        httpc:set_timeout(2000)

        -- Connect directly using injected IP and port
        local conn_ok, conn_err = httpc:connect({
            host = AGENT_IP,
            port = AGENT_PORT,
            scheme = "http",
        })

        if not conn_ok then
            ngx.log(ngx.ERR, "Failed to connect to agent: ", conn_err)
            return
        end

        local res, req_err = httpc:request({
            method = "POST",
            path = "/api/security/events/ingest",
            body = body,
            headers = {
                ["Host"] = AGENT_IP .. ":" .. AGENT_PORT,
                ["Content-Type"] = "application/json",
            }
        })

        if not res then
            ngx.log(ngx.ERR, "Failed to send security event: ", req_err)
        end

        httpc:close()
    end)

    if not ok then
        ngx.log(ngx.ERR, "Failed to create timer for security event: ", err)
    end
end

-- Rate limiting helper using shared dict
function _M.check_rate_limit(key, limit, window)
    local dict = ngx.shared.ip_rate_limit
    if not dict then return false end

    local current = dict:get(key)
    if not current then
        dict:set(key, 1, window)
        return false
    end

    if current >= limit then
        return true
    end

    dict:incr(key, 1)
    return false
end

-- Internal API handlers for immediate IP blocking

local function json_response(status, data)
    ngx.status = status
    ngx.header["Content-Type"] = "application/json"
    ngx.say(cjson.encode(data))
end

-- Handle block IP request from agent
function _M.handle_block_ip_request()
    local client_ip = ngx.var.remote_addr

    if ngx.req.get_method() ~= "POST" then
        ngx.log(ngx.WARN, "block-ip: method not allowed from ", client_ip)
        json_response(405, {error = "Method not allowed"})
        return
    end

    ngx.req.read_body()
    local body = ngx.req.get_body_data()
    if not body then
        ngx.log(ngx.ERR, "block-ip: no body from ", client_ip)
        json_response(400, {error = "No body provided"})
        return
    end

    local data, err = cjson.decode(body)
    if not data then
        ngx.log(ngx.ERR, "block-ip: invalid JSON from ", client_ip, ": ", err, " body=", body:sub(1, 100))
        json_response(400, {error = "Invalid JSON: " .. (err or "unknown")})
        return
    end

    local ip = data.ip
    local ttl = data.ttl or 86400  -- default 24 hours

    if not ip then
        ngx.log(ngx.ERR, "block-ip: missing IP in request from ", client_ip)
        json_response(400, {error = "IP address required"})
        return
    end

    local dict = ngx.shared.blocked_ips
    if not dict then
        ngx.log(ngx.ERR, "block-ip: shared dict not available")
        json_response(500, {error = "Shared dict not available"})
        return
    end

    local ok, set_err = dict:set("ip:" .. ip, true, ttl)
    if not ok then
        ngx.log(ngx.ERR, "block-ip: failed to set IP ", ip, " in dict: ", set_err)
        json_response(500, {error = "Failed to block IP: " .. (set_err or "unknown")})
        return
    end

    ngx.log(ngx.INFO, "block-ip: blocked ", ip, " for ", ttl, "s")
    json_response(200, {success = true, ip = ip, ttl = ttl})
end

-- Handle unblock IP request from agent
function _M.handle_unblock_ip_request()
    local client_ip = ngx.var.remote_addr

    if ngx.req.get_method() ~= "POST" then
        ngx.log(ngx.WARN, "unblock-ip: method not allowed from ", client_ip)
        json_response(405, {error = "Method not allowed"})
        return
    end

    ngx.req.read_body()
    local body = ngx.req.get_body_data()
    if not body then
        ngx.log(ngx.ERR, "unblock-ip: no body from ", client_ip)
        json_response(400, {error = "No body provided"})
        return
    end

    local data, err = cjson.decode(body)
    if not data then
        ngx.log(ngx.ERR, "unblock-ip: invalid JSON from ", client_ip, ": ", err, " body=", body:sub(1, 100))
        json_response(400, {error = "Invalid JSON: " .. (err or "unknown")})
        return
    end

    local ip = data.ip
    if not ip then
        ngx.log(ngx.ERR, "unblock-ip: missing IP in request from ", client_ip)
        json_response(400, {error = "IP address required"})
        return
    end

    local dict = ngx.shared.blocked_ips
    if not dict then
        ngx.log(ngx.ERR, "unblock-ip: shared dict not available")
        json_response(500, {error = "Shared dict not available"})
        return
    end

    dict:delete("ip:" .. ip)
    ngx.log(ngx.INFO, "unblock-ip: unblocked ", ip)
    json_response(200, {success = true, ip = ip})
end

-- Handle refresh request - force full cache refresh
function _M.handle_refresh_request()
    local client_ip = ngx.var.remote_addr

    if ngx.req.get_method() ~= "POST" then
        ngx.log(ngx.WARN, "refresh: method not allowed from ", client_ip)
        json_response(405, {error = "Method not allowed"})
        return
    end

    ngx.log(ngx.INFO, "refresh: refreshing blocked IPs cache")
    _M.refresh_blocked_ips()
    json_response(200, {success = true, message = "Cache refreshed"})
end

return _M
