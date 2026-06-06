-- FlatRun Security Event Capture (E2E Test Version)
-- NOTE: This is a simplified version for e2e testing that uses environment variables.
-- The production template is at: templates/infra/nginx/lua/security.lua
-- Keep core functionality in sync with the template.

local cjson = require "cjson.safe"
local http = require "resty.http"

local _M = {}

-- Configuration via environment variable (test-specific)
local AGENT_URL = os.getenv("FLATRUN_AGENT_URL") or "http://host.docker.internal:8080"
local INTERNAL_TOKEN = os.getenv("FLATRUN_INTERNAL_TOKEN") or ""

-- Blocked IPs cache settings
local BLOCKED_IPS_CACHE_TTL = 30  -- seconds
local BLOCKED_IPS_LAST_FETCH = "blocked_ips_last_fetch"

-- Check if an IP is blocked (with caching)
function _M.is_blocked(ip)
    if not ip then return false end

    local dict = ngx.shared.blocked_ips
    if not dict then return false end

    local is_blocked = dict:get("ip:" .. ip)
    if is_blocked ~= nil then
        return is_blocked
    end

    local last_fetch = dict:get(BLOCKED_IPS_LAST_FETCH) or 0
    local now = ngx.time()

    if now - last_fetch > BLOCKED_IPS_CACHE_TTL then
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

    local res, err = httpc:request_uri(AGENT_URL .. "/api/_internal/blocked-ips", {
        method = "GET",
        headers = {
            ["X-Internal-Token"] = INTERNAL_TOKEN,
        },
    })

    if not res then
        ngx.log(ngx.ERR, "Failed to fetch blocked IPs: ", err)
        return
    end

    if res.status ~= 200 then
        ngx.log(ngx.ERR, "Blocked IPs API returned status: ", res.status)
        return
    end

    local data, decode_err = cjson.decode(res.body)
    if not data then
        ngx.log(ngx.ERR, "Failed to decode blocked IPs response: ", decode_err)
        return
    end

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

        local httpc = http.new()
        httpc:set_timeout(5000)

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

        local res, req_err = httpc:request_uri(AGENT_URL .. "/api/security/events/ingest", {
            method = "POST",
            body = body,
            headers = {
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
-- NOTE: Keep in sync with templates/infra/nginx/lua/security.lua

local function json_response(status, data)
    ngx.status = status
    ngx.header["Content-Type"] = "application/json"
    ngx.say(cjson.encode(data))
end

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
    local ttl = data.ttl or 86400

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
