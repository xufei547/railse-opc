# Human approval steps in n8n, routed to your phone via Railse

[中文](n8n.md)

## The scenario

An n8n workflow reaches a step that needs a human to say yes before it goes on: publishing a post, sending a customer a payment reminder, issuing a refund. n8n's own Wait node can pause for an HTTP request, but nothing tells you it is waiting, and you have to be at a computer. The Railse hub has a webhook endpoint for this. The workflow posts an approval request to the hub, the hub pushes it to your phone, you tap approve or deny, the hub calls n8n back and the workflow continues. The request sits in the same inbox as your Claude Code and Codex approvals.

## What you need

1. **The hub URL.** The address you open the inbox with on your phone, for example `https://hub.example.com`. n8n must be able to reach it, and the hub must be able to reach n8n, because the callback is an HTTP request the hub makes. If both run on the same server, a private or `http://127.0.0.1:5678/...` callback address is fine; the hub does not block private networks.
2. **A source token.** On the machine that runs the hub:

   ```
   railse hub webhook-token
   ```

   The plaintext is shown once; the hub stores only a hash. If you lose it, run the command again and the old token stops working. It takes effect at once, no restart. Until a token exists, the webhook endpoint answers 503.

## In n8n

Four nodes: a trigger, HTTP Request, Wait, IF.

**HTTP Request node**: posts the approval to the hub.

- Method `POST`, URL `https://hub.example.com/api/webhook/approvals`
- Header `Authorization: Bearer <source token>`
- Body (JSON):

  ```json
  {
    "title": "Publish newsletter #42",
    "description": "Optional, up to 4096 characters, shown under the title on the phone",
    "callback_url": "{{ $execution.resumeUrl }}",
    "callback_token": "Optional, sent back as-is in the Authorization header",
    "expires_in": 3600
  }
  ```

Fields: `title` is required, at most 200 characters, no control characters. `callback_url` is required and must be http or https; put n8n's resume URL for this execution here, `{{ $execution.resumeUrl }}`, which is what wakes the Wait node. `callback_token` is optional, at most 512 bytes of visible ASCII. `expires_in` is optional, 1 to 86400 seconds, default 24 hours. The body may be at most 64 KiB.

On success the hub answers `200` with `{"id": "<request id>"}`. A bad field gives 400, a wrong token 401, no token configured 503.

**Wait node**: set Resume to **On Webhook Call** and HTTP Method to **POST** (the default is not POST). Turn on Limit Wait Time and set it a little longer than `expires_in`, so the workflow does not hang forever if a callback never arrives.

**IF node**: when the hub's callback comes out of the Wait node, the request body is under `$json.body`. The condition is:

```
{{ $json.body.decision }}   equals   allow
```

Wire the real action to the true branch and your "denied" handling to the false branch. The callback body has exactly three fields:

```json
{"id": "<request id>", "decision": "allow", "note": "go ahead"}
```

`decision` is either `allow` or `deny`. `note` is the note you typed on the phone, or an empty string. The callback is sent with `Content-Type: application/json`; if you gave a `callback_token`, it carries `Authorization: Bearer <callback_token>`, which you can check with Header Auth on the Wait node.

## What you see on the phone

One notification, titled "A workflow is waiting for your approval", with your `title` as the body. Tapping it opens the request page: title, description, Approve and Deny buttons, and an optional note that goes to n8n in the callback. Remember is not available for webhook requests: there is no command to learn a rule from, so each one needs a human tap.

## Timeouts and failures

- **Expiry.** If nobody decides before `expires_in` (default 24 hours), the hub marks the request `expired` and still calls back once:

  ```json
  {"id": "<request id>", "decision": "deny", "note": "expired"}
  ```

  So the IF node needs no separate timeout check; expiry takes the false branch. To tell "a person denied it" from "nobody looked", check whether `note` is `expired`.
- **Callback retries.** Each attempt times out after 10 seconds, only a 2xx counts as success, and redirects are not followed. After a failure the hub waits 2 seconds and tries again, 3 attempts in all. After the third failure it gives up and does not resend; the hub's audit log gets a `webhook_callback` entry with `ok: false`.
- **Hub restart.** A callback in flight is aborted and not resumed after restart; the source has to submit again. This is why the Wait node should have Limit Wait Time set.
- **Withdrawing.** There is no endpoint to cancel a submitted request; use a shorter `expires_in` and let it expire.

## A minimal workflow you can import

The JSON below imports into n8n as is (Workflows → Import from File / from URL, or paste it onto the canvas). Replace the two values that start with `REPLACE_`.

> The workflow below targets n8n 1.x node versions. Import it and run it once in your own n8n before wiring it into anything real.

```json
{
  "name": "Railse approval (minimal)",
  "nodes": [
    {
      "parameters": {},
      "name": "Manual Trigger",
      "type": "n8n-nodes-base.manualTrigger",
      "typeVersion": 1,
      "position": [0, 0]
    },
    {
      "parameters": {
        "method": "POST",
        "url": "REPLACE_HUB_URL/api/webhook/approvals",
        "sendHeaders": true,
        "headerParameters": {
          "parameters": [
            { "name": "Authorization", "value": "Bearer REPLACE_WEBHOOK_TOKEN" }
          ]
        },
        "sendBody": true,
        "specifyBody": "json",
        "jsonBody": "={ \"title\": \"Publish newsletter\", \"description\": \"Human approval from n8n\", \"callback_url\": \"{{ $execution.resumeUrl }}\", \"expires_in\": 3600 }",
        "options": {}
      },
      "name": "Ask Railse",
      "type": "n8n-nodes-base.httpRequest",
      "typeVersion": 4.2,
      "position": [220, 0]
    },
    {
      "parameters": {
        "resume": "webhook",
        "httpMethod": "POST",
        "options": {}
      },
      "name": "Wait for decision",
      "type": "n8n-nodes-base.wait",
      "typeVersion": 1.1,
      "position": [440, 0]
    },
    {
      "parameters": {
        "conditions": {
          "options": { "caseSensitive": true, "leftValue": "", "typeValidation": "strict" },
          "conditions": [
            {
              "id": "decision-is-allow",
              "leftValue": "={{ $json.body.decision }}",
              "rightValue": "allow",
              "operator": { "type": "string", "operation": "equals" }
            }
          ],
          "combinator": "and"
        },
        "options": {}
      },
      "name": "Approved?",
      "type": "n8n-nodes-base.if",
      "typeVersion": 2,
      "position": [660, 0]
    }
  ],
  "connections": {
    "Manual Trigger": { "main": [[{ "node": "Ask Railse", "type": "main", "index": 0 }]] },
    "Ask Railse": { "main": [[{ "node": "Wait for decision", "type": "main", "index": 0 }]] },
    "Wait for decision": { "main": [[{ "node": "Approved?", "type": "main", "index": 0 }]] }
  }
}
```

Values to replace: `REPLACE_HUB_URL` (the hub address, no path) and `REPLACE_WEBHOOK_TOKEN` (the output of `railse hub webhook-token`). A token written into the node is exported with the workflow; for real use, move it into an n8n Header Auth credential. After importing, click Execute workflow; a notification should appear on your phone, and after you approve, the IF node's true branch lights up.

The full endpoint reference is in the webhook section of [hub.md](../hub.md) (Chinese).
