-- FlatRun Security Event Capture
-- This script captures security-relevant events and sends them to the agent API

local cjson = require "cjson.safe"
local http = require "resty.http"

local _M = {}

-- Configuration (injected by agent during deployment)
local AGENT_IP = "{{.AgentIP}}"
local AGENT_PORT = {{.AgentPort}}

-- Blocked IPs cache settings
local BLOCKED_IPS_CACHE_TTL = 30  -- seconds
local BLOCKED_IPS_CACHE_KEY = "blocked_ips_list"
local BLOCKED_IPS_LAST_FETCH = "blocked_ips_last_fetch"

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

-- Check if an IP is blocked (with caching)
function _M.is_blocked(ip)
    if not ip then return false end

    local dict = ngx.shared.blocked_ips
    if not dict then return false end

    -- Check if this specific IP is marked as blocked
    local is_blocked = dict:get("ip:" .. ip)
    if is_blocked ~= nil then
        return is_blocked
    end

    -- Check if we need to refresh the cache
    local last_fetch = dict:get(BLOCKED_IPS_LAST_FETCH) or 0
    local now = ngx.time()

    if now - last_fetch > BLOCKED_IPS_CACHE_TTL then
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
        path = "/api/security/blocked-ips",
        headers = {
            ["Host"] = AGENT_IP .. ":" .. AGENT_PORT,
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
            dict:set("ip:" .. entry.ip, true, BLOCKED_IPS_CACHE_TTL * 2)
        end
    end

    ngx.log(ngx.INFO, "Refreshed blocked IPs cache: ", #blocked_ips, " IPs")
end

-- Initialize blocked IPs cache on worker start
function _M.init_blocked_ips()
    ngx.timer.at(0, function()
        _M.refresh_blocked_ips()
    end)
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
    local ip = ngx.var.remote_addr
    local method = ngx.var.request_method
    local user_agent = ngx.var.http_user_agent or ""
    local host = ngx.var.host or ""

    -- Only capture security-relevant events
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

-- Handle block IP request from agent
function _M.handle_block_ip_request()
    if ngx.req.get_method() ~= "POST" then
        ngx.status = 405
        ngx.say('{"error": "Method not allowed"}')
        return
    end

    ngx.req.read_body()
    local body = ngx.req.get_body_data()
    if not body then
        ngx.status = 400
        ngx.say('{"error": "No body provided"}')
        return
    end

    local data, err = cjson.decode(body)
    if not data then
        ngx.status = 400
        ngx.say('{"error": "Invalid JSON: ' .. (err or "unknown") .. '"}')
        return
    end

    local ip = data.ip
    local ttl = data.ttl or 86400  -- default 24 hours

    if not ip then
        ngx.status = 400
        ngx.say('{"error": "IP address required"}')
        return
    end

    local dict = ngx.shared.blocked_ips
    if not dict then
        ngx.status = 500
        ngx.say('{"error": "Shared dict not available"}')
        return
    end

    local ok, set_err = dict:set("ip:" .. ip, true, ttl)
    if not ok then
        ngx.status = 500
        ngx.say('{"error": "Failed to block IP: ' .. (set_err or "unknown") .. '"}')
        return
    end

    ngx.status = 200
    ngx.header["Content-Type"] = "application/json"
    ngx.say('{"success": true, "ip": "' .. ip .. '", "ttl": ' .. ttl .. '}')
end

-- Handle unblock IP request from agent
function _M.handle_unblock_ip_request()
    if ngx.req.get_method() ~= "POST" then
        ngx.status = 405
        ngx.say('{"error": "Method not allowed"}')
        return
    end

    ngx.req.read_body()
    local body = ngx.req.get_body_data()
    if not body then
        ngx.status = 400
        ngx.say('{"error": "No body provided"}')
        return
    end

    local data, err = cjson.decode(body)
    if not data then
        ngx.status = 400
        ngx.say('{"error": "Invalid JSON: ' .. (err or "unknown") .. '"}')
        return
    end

    local ip = data.ip
    if not ip then
        ngx.status = 400
        ngx.say('{"error": "IP address required"}')
        return
    end

    local dict = ngx.shared.blocked_ips
    if not dict then
        ngx.status = 500
        ngx.say('{"error": "Shared dict not available"}')
        return
    end

    dict:delete("ip:" .. ip)

    ngx.status = 200
    ngx.header["Content-Type"] = "application/json"
    ngx.say('{"success": true, "ip": "' .. ip .. '"}')
end

-- Handle refresh request - force full cache refresh
function _M.handle_refresh_request()
    if ngx.req.get_method() ~= "POST" then
        ngx.status = 405
        ngx.say('{"error": "Method not allowed"}')
        return
    end

    -- Perform synchronous refresh
    _M.refresh_blocked_ips()

    ngx.status = 200
    ngx.header["Content-Type"] = "application/json"
    ngx.say('{"success": true, "message": "Cache refreshed"}')
end

return _M
