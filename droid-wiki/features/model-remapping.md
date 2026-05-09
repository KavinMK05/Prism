# Model remapping

Active contributors: KavinMK05

## Purpose

Prism can rewrite model names on the fly, so clients can request models using names that don't exist on the upstream provider. For example, a client sends `claude-3-5-haiku` and Prism forwards it as `deepseek-v4-flash:cloud`.

## Configuration

Model remapping is stored in `%APPDATA%\prism\model_remapping.json`:

```json
{
  "default_model": "glm-5.1:cloud",
  "known_models": [
    "glm-5.1:cloud",
    "deepseek-v4-flash:cloud",
    "opencode/deepseek-v4-flash",
    "deepseek-v4-pro:cloud"
  ],
  "aliases": {
    "claude-3-5-haiku": "deepseek-v4-flash:cloud",
    "claude-3-5-haiku-20241022": "deepseek-v4-flash:cloud"
  }
}
```

## Resolution logic

The `getEffectiveModel` function in `config.go` applies this resolution order:

1. Check if the requested model matches an alias → return the alias target
2. Check if the requested model is in the known models list → return as-is
3. Check if the requested model starts with a known model prefix (e.g., `deepseek-v4-flash:cloud-xyz`) → return as-is
4. Fall back to the default model → return `defaultModel`
5. If no default is set → return the original model name unmodified

A log line is emitted for every remapping: `[map] Model remap (reason): original -> mapped`.

## Admin UI

The admin UI's **Models** tab provides a web editor for the model remapping configuration. The API endpoints at `/admin/model-remap` accept GET and PUT requests for reading and saving the remapping.

## Key source files

| File | Purpose |
|---|---|
| `config.go` | `getEffectiveModel`, `loadModelRemapping`, `saveModelRemapping` |
| `admin.go` | `/admin/model-remap` API endpoint |
