-- FlatRun Security Event Capture
-- This script captures security-relevant events and sends them to the agent API

local cjson = require "cjson.safe"
local http = require "resty.http"

local _M = {}

-- Configuration (will be set by the agent)
local AGENT_URL = os.getenv("FLATRUN_AGENT_URL") or "http://host.docker.internal:8090"

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

return _M
