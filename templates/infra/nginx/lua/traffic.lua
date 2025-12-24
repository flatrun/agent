-- FlatRun Traffic Logging
-- This script logs all requests to the agent API for traffic statistics

local cjson = require "cjson.safe"
local http = require "resty.http"
local security = require "security"

local _M = {}

-- Configuration (injected by agent during deployment)
local AGENT_IP = "{{.AgentIP}}"
local AGENT_PORT = {{.AgentPort}}

function _M.log_request()
    local uri = ngx.var.uri or ""
    local ip = security.get_client_ip()

    if security.is_whitelisted(ip, uri) then return end

    local status = ngx.status
    local host = ngx.var.host or ""
    local method = ngx.var.request_method or ""
    local request_time = ngx.var.request_time or "0"
    local bytes_sent = ngx.var.bytes_sent or "0"
    local request_length = ngx.var.request_length or "0"
    local upstream_response_time = ngx.var.upstream_response_time or ""

    -- Extract deployment name from host (remove port if present)
    local deployment_name = host:match("^([^:]+)") or host

    -- Non-blocking: fire and forget via timer
    local ok, err = ngx.timer.at(0, function(premature)
        if premature then return end

        local body, encode_err = cjson.encode({
            deployment_name = deployment_name,
            request_path = uri,
            request_method = method,
            status_code = status,
            source_ip = ip,
            response_time_ms = math.floor((tonumber(request_time) or 0) * 1000),
            bytes_sent = tonumber(bytes_sent) or 0,
            request_length = tonumber(request_length) or 0,
            upstream_time_ms = upstream_response_time ~= "" and math.floor((tonumber(upstream_response_time) or 0) * 1000) or nil,
            timestamp = ngx.time()
        })

        if not body then
            ngx.log(ngx.ERR, "Failed to encode traffic log: ", encode_err)
            return
        end

        local httpc = http.new()
        httpc:set_timeout(2000)

        local conn_ok, conn_err = httpc:connect({
            host = AGENT_IP,
            port = AGENT_PORT,
            scheme = "http",
        })

        if not conn_ok then
            ngx.log(ngx.ERR, "Failed to connect to agent for traffic log: ", conn_err)
            return
        end

        local res, req_err = httpc:request({
            method = "POST",
            path = "/api/traffic/ingest",
            body = body,
            headers = {
                ["Host"] = AGENT_IP .. ":" .. AGENT_PORT,
                ["Content-Type"] = "application/json",
            }
        })

        if not res then
            ngx.log(ngx.ERR, "Failed to send traffic log: ", req_err)
        end

        httpc:close()
    end)

    if not ok then
        ngx.log(ngx.ERR, "Failed to create timer for traffic log: ", err)
    end
end

return _M
