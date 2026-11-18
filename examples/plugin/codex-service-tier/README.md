# Codex Service Tier Plugin

This plugin is a request normalizer for Codex outbound requests.

When the plugin is enabled and `fast` is set to `true`, it sets the top-level `service_tier` field to `priority` for requests where:

- `req.ToFormat` is `codex`
- `req.Model` is `gpt-5.5`

Requests that do not match these conditions are returned unchanged.

## Configuration

Add the plugin under `plugins.configs`:

```yaml
plugins:
  configs:
    codex-service-tier:
      enabled: true
      priority: 1
      fast: false
```

`fast` is a boolean field. Set it to `true` to enable priority service tier shaping for matching Codex `gpt-5.5` requests.

The tier this plugin sets counts as the client tier for the routing profile's Codex speed policy: a profile with `ignore-client-tier: true` removes it, and with `false` it is kept instead of the profile speed. See [Codex speed](../../../docs/management-api-v8.md#codex-speed).
