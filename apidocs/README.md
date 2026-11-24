# TruERP Public API — maintainer notes

The integration API for external platforms. The **contract** is `openapi.yaml`
(served at `/api/public/v1/openapi.yaml|json`). The rendered docs are at
`/api/public/docs` (Redoc) and `/api/public/docs/explorer` (Swagger UI).

## Layout

| Path | Role |
|---|---|
| `apidocs/openapi.yaml` | OpenAPI 3.1 spec (source of truth for integrators) |
| `routes/public_api.go` | Route table: public API, docs, super-admin management |
| `middleware/public_auth.go` | API key / OAuth auth, store policy, rate limit, request log |
| `controllers/public_api.go` | Pagination, errors, idempotency, in-process delegation |
| `controllers/public_representations.go` | Public JSON shapes (stable contract) |
| `controllers/public_resources.go` | Parties, products, stock, invoices, payments, purchase bills |
| `controllers/public_webhooks.go` | Event feed (change scanner), webhooks, delivery worker |
| `controllers/integration_admin.go` | Super-admin API: keys, policies, OAuth apps, webhooks, logs |
| `controllers/oauth.go` | OAuth 2.0 authorization server |
| `utils/scopes.go` | Scope catalog |

Writes call the internal `/api/v1` handlers in-process (`callInternal`), so stock,
balances, ledgers and numbering behave exactly as in the apps. Reads query the DB and
map to the public representations.

## Rules for changes

* Add fields; never rename or remove them. Breaking changes need `/api/public/v2`.
* Every public route must be in `openapi.yaml`; `TestOpenAPISpecIsValidAndCoversRoutes`
  fails otherwise (in both directions).
* New resource → add read/write scopes in `utils/scopes.go`; they then appear in the
  admin permission matrix automatically.

## Environment

| Variable | Purpose |
|---|---|
| `PUBLIC_API_BASE_URL` | Public origin used in docs and OAuth metadata, e.g. `https://backend.truerp.co.in`. Defaults to the request host. |
| `TRUERP_MCP_CLIENT_SECRET` | Provisions the `truerp-mcp-client` OAuth client for the MCP server. Without it, a client still using the old published default secret is **deactivated** at startup. |
| `WEBHOOKS_ALLOW_PRIVATE_NETWORKS` | `true` only in local development: allows `http://` and private/loopback webhook URLs. |

## Operational notes

* Webhooks/event feed: a scanner runs every 15 s per store that has a webhook
  endpoint, an API key or a store-bound OAuth app, and turns changed rows into
  events. Hard deletes (no `deleted_at`) do not produce events.
* Events, deliveries and request logs are kept 30 days; idempotency records 24 h.
* Rate limits are in-memory per process. With several backend replicas the effective
  limit is multiplied by the replica count.
