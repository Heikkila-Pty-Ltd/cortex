# Cortex API Security

This document describes the authentication, authorization, and audit logging features for Cortex API endpoints.

## Overview

Cortex provides a lightweight HTTP API for monitoring and controlling the scheduler. Security controls protect control endpoints that can modify system state while leaving read-only monitoring endpoints accessible.

## Security Model

### Endpoint Classification

**Read-only endpoints** (no authentication required):
- `GET /status` - System status and uptime
- `GET /health` - Health check status  
- `GET /metrics` - Prometheus metrics
- `GET /projects` - Project configuration
- `GET /projects/{id}` - Project details
- `GET /teams` - Team information
- `GET /teams/{project}` - Project team details
- `GET /dispatches/{bead_id}` - Dispatch history (read-only)
- `GET /scheduler/status` - Scheduler status
- `GET /recommendations` - System recommendations

**Control endpoints** (authentication required):
- `POST /scheduler/pause` - Pause the scheduler
- `POST /scheduler/resume` - Resume the scheduler
- `POST /dispatches/{id}/cancel` - Cancel running dispatch
- `POST /dispatches/{id}/retry` - Retry failed dispatch

## Configuration

Add security configuration to your `cortex.toml`:

```toml
[api]
bind = "0.0.0.0:8080"

[api.security]
# Enable authentication for control endpoints
enabled = true

# Valid API tokens (minimum 16 characters each)
allowed_tokens = [
    "your-secure-token-here-1234567890",
    "another-token-for-different-client"
]

# Require local connections when auth is disabled (default: true for non-local bind)
require_local_only = false

# Path for audit log (optional)
audit_log = "/var/log/cortex/api-audit.log"
```

### Security Modes

1. **Authentication Disabled + Local Only** (default for non-local bind):
   ```toml
   [api.security]
   enabled = false
   require_local_only = true
   ```
   - Control endpoints only accept local connections (127.0.0.1, ::1, private IPs)
   - Suitable for single-host deployments

2. **Authentication Disabled + Open** (only for localhost bind):
   ```toml
   [api.security]
   enabled = false
   require_local_only = false
   ```
   - No authentication required
   - Only safe when binding to localhost

3. **Token Authentication** (recommended for remote access):
   ```toml
   [api.security]
   enabled = true
   allowed_tokens = ["secure-token-123..."]
   audit_log = "/var/log/cortex/audit.log"
   ```
   - Bearer token authentication required for control endpoints
   - Audit logging of all control operations

## API Authentication

When authentication is enabled, control endpoints require a valid Bearer token:

```bash
# Pause scheduler
curl -X POST \
  -H "Authorization: Bearer your-secure-token-here-1234567890" \
  http://cortex-api:8080/scheduler/pause

# Cancel dispatch
curl -X POST \
  -H "Authorization: Bearer your-secure-token-here-1234567890" \
  http://cortex-api:8080/dispatches/1234/cancel
```

Invalid or missing tokens return `401 Unauthorized`:

```json
{
  "error": "Unauthorized: valid token required"
}
```

## Audit Logging

When `audit_log` is configured, all control endpoint requests are logged in JSON format:

```json
{
  "timestamp": "2026-02-18T10:30:00Z",
  "remote_addr": "192.168.1.100:54321",
  "method": "POST",
  "path": "/scheduler/pause",
  "user_agent": "curl/7.68.0",
  "authorized": true,
  "token": "your-****",
  "status_code": 200,
  "duration": "15.2ms"
}
```

Fields:
- `timestamp` - Request timestamp (ISO 8601)
- `remote_addr` - Client IP and port
- `method` - HTTP method
- `path` - Request path
- `user_agent` - Client user agent (if provided)
- `authorized` - Whether request was authorized
- `token` - Truncated token (first 4 chars + "****")
- `error` - Error message (if request failed)
- `status_code` - HTTP response code
- `duration` - Request processing time

## Deployment Patterns

### Local Development

```toml
[api]
bind = "127.0.0.1:8080"

[api.security]
enabled = false
```

Safe for local development - only localhost connections accepted.

### Single Host Production

```toml
[api]
bind = "0.0.0.0:8080"

[api.security]
enabled = false
require_local_only = true
audit_log = "/var/log/cortex/audit.log"
```

Accept connections from local network but reject external requests.

### Multi-Host Production

```toml
[api]
bind = "0.0.0.0:8080"

[api.security]
enabled = true
allowed_tokens = [
    "prod-monitoring-token-abcd1234567890",
    "ops-control-token-xyz9876543210"
]
audit_log = "/var/log/cortex/audit.log"
```

Full authentication with audit logging for production environments.

### Reverse Proxy with TLS

For production deployments, place Cortex behind a reverse proxy:

```nginx
server {
    listen 443 ssl;
    server_name cortex-api.company.com;
    
    ssl_certificate /etc/ssl/certs/cortex.crt;
    ssl_certificate_key /etc/ssl/private/cortex.key;
    
    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Host $host;
    }
}
```

## Token Management

### Generation

Generate secure tokens with sufficient entropy:

```bash
# Option 1: OpenSSL
openssl rand -base64 32

# Option 2: Python
python3 -c "import secrets; print(secrets.token_urlsafe(32))"

# Option 3: Node.js
node -e "console.log(require('crypto').randomBytes(32).toString('base64url'))"
```

### Rotation

To rotate tokens:

1. Add new token to `allowed_tokens` array
2. Reload configuration: `systemctl reload cortex`
3. Update clients to use new token
4. Remove old token from configuration
5. Reload configuration again

### Storage

Store tokens securely:
- Use configuration management (Ansible, Chef, etc.)
- Mount secrets as files and reference them
- Use environment variables for containerized deployments

## Monitoring

### Metrics

Monitor authentication failures via logs or metrics:

```bash
# Count failed auth attempts in last hour
grep '"authorized":false' /var/log/cortex/audit.log | \
  grep "$(date -d '1 hour ago' '+%Y-%m-%d')" | wc -l
```

### Alerts

Set up alerts for:
- High rate of authentication failures
- Unexpected source IPs accessing control endpoints
- Control operations outside business hours

## Security Considerations

1. **Token Security**: Use long, random tokens (32+ chars recommended)
2. **Transport Security**: Use TLS in production (reverse proxy recommended)
3. **Network Security**: Restrict network access to API port via firewall
4. **Audit Retention**: Rotate audit logs and comply with retention policies
5. **Monitoring**: Alert on authentication failures and unexpected usage patterns

## Troubleshooting

### Authentication Not Working

Check configuration:
```bash
# Verify config syntax
cortex -config cortex.toml -dry-run

# Check logs for auth errors
journalctl -u cortex -f | grep auth
```

### Local-Only Mode Issues

Verify client IP classification:
- `127.0.0.1` - Always local
- `::1` - Always local  
- RFC 1918 ranges (`10.x`, `172.16-31.x`, `192.168.x`) - Considered local
- Other IPs - Considered remote

### Audit Log Not Writing

Check permissions and directory:
```bash
# Ensure directory exists and is writable
sudo mkdir -p /var/log/cortex
sudo chown cortex:cortex /var/log/cortex
sudo chmod 755 /var/log/cortex
```