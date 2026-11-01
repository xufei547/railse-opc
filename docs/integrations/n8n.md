# 用 n8n 把人工审批接到 Railse

[English](n8n.en.md)

## 场景

n8n 里有一条工作流，跑到某一步必须有人点头才能往下走：发一篇帖子、给客户发一封催款邮件、把一笔退款打出去。n8n 自带的 Wait 节点能停下来等一个 HTTP 请求，但没人去催你，你也得守着电脑。Railse 的 hub 有一个 webhook 入口：工作流把审批请求提交给 hub，hub 推到手机，你点批准或拒绝，hub 把结果回调给 n8n，工作流接着跑。它和 Claude Code、Codex 的审批排在同一个收件箱里。

## 准备

1. **hub 地址。** 手机打开收件箱用的那个地址，例如 `https://hub.example.com`。n8n 要能连到它；hub 也要能连到 n8n（回调是 hub 主动发起的 HTTP 请求）。两者在同一台服务器上时，回调地址用内网或 `http://127.0.0.1:5678/...` 也可以，hub 不限制私网地址。
2. **来源令牌。** 在跑 hub 的机器上执行：

   ```
   railse hub webhook-token
   ```

   明文只显示这一次，hub 里只存哈希；丢了再生成一次，旧令牌随之失效。立即生效，不用重启 hub。没生成过令牌时，webhook 接口回 503。

## 在 n8n 里

四个节点：触发器 → HTTP Request → Wait → IF。

**HTTP Request 节点**：向 hub 提交审批。

- 方法 `POST`，地址 `https://hub.example.com/api/webhook/approvals`
- 请求头 `Authorization: Bearer <来源令牌>`
- 请求体（JSON）：

  ```json
  {
    "title": "发布周报《第 42 期》",
    "description": "可选，至多 4096 字符，手机上展示在标题下面",
    "callback_url": "{{ $execution.resumeUrl }}",
    "callback_token": "可选，回调时原样放进 Authorization 头",
    "expires_in": 3600
  }
  ```

字段说明：`title` 必填，至多 200 字符，不能含控制字符；`callback_url` 必填，只收 http/https，这里填 n8n 本次执行的恢复地址 `{{ $execution.resumeUrl }}`，Wait 节点就靠它被唤醒；`callback_token` 可选，至多 512 字节、只能是可见 ASCII；`expires_in` 可选，1 到 86400 秒，不填默认 24 小时。请求体至多 64 KiB。

成功时 hub 回 `200` 和 `{"id": "<请求 id>"}`。字段不合格回 400，令牌不对回 401，没配令牌回 503。

**Wait 节点**：Resume 选 **On Webhook Call**，HTTP Method 改成 **POST**（默认不是 POST）。建议把 Limit Wait Time 打开，设得比 `expires_in` 略长，免得回调万一没到，工作流一直挂着。

**IF 节点**：hub 的回调经 Wait 节点流出来时，请求体在 `$json.body` 下。条件写：

```
{{ $json.body.decision }}   等于   allow
```

true 分支接真正要做的事，false 分支接“被拒绝”的处理。回调体一共三个字段：

```json
{"id": "<请求 id>", "decision": "allow", "note": "去吧"}
```

`decision` 只有 `allow` 和 `deny` 两个值；`note` 是你在手机上写的备注，没写就是空串。回调带 `Content-Type: application/json`；你给了 `callback_token`，就带 `Authorization: Bearer <callback_token>`，可以在 Wait 节点里用 Header Auth 校验它。

## 手机上看到什么

一条通知，标题是「工作流等你批准」，正文是你给的 `title`。点开是请求页：标题、描述、批准和拒绝两个按钮，可以写一句备注，备注随回调发给 n8n。「记住」对 webhook 请求不可用：它不是命令，没法学成规则，每次都要人点。

## 超时与失败

- **过期。** 到了 `expires_in`（默认 24 小时）还没人处理，hub 把请求置为 `expired`，并照样回调一次：

  ```json
  {"id": "<请求 id>", "decision": "deny", "note": "expired"}
  ```

  所以 IF 节点不用单独判断超时，走 false 分支即可；要区分“人拒绝”和“没人管”，再看一眼 `note` 是不是 `expired`。
- **回调重试。** 每次回调 10 秒超时，只认 2xx，不跟随重定向；失败后等 2 秒再试，最多 3 次。3 次都失败就放弃，不再补发，hub 的审计里记一条 `webhook_callback`、`ok: false`。
- **hub 重启。** 正在进行的回调中止，重启后不恢复；来源需要重新提交。这就是 Wait 节点要设 Limit Wait Time 的原因。
- **撤回。** 没有接口撤回已提交的请求，用短一点的 `expires_in` 让它自己过期。

## 最小可复制的工作流

下面的 JSON 可以直接导入 n8n（Workflows → Import from File / from URL，或在画布上粘贴）。`REPLACE_` 开头的两处要换成你自己的。

> 下面这份 workflow 按 n8n 1.x 的节点版本写，导入后请先在你自己的 n8n 里走一遍再接到正式流程。

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
        "jsonBody": "={ \"title\": \"发布周报\", \"description\": \"来自 n8n 的人工审批\", \"callback_url\": \"{{ $execution.resumeUrl }}\", \"expires_in\": 3600 }",
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

要替换的值：`REPLACE_HUB_URL`（hub 地址，不带路径）、`REPLACE_WEBHOOK_TOKEN`（`railse hub webhook-token` 的输出）。令牌写在节点里会随工作流一起导出，正式用时改成 n8n 的 Header Auth 凭据。导入后点 Execute workflow，手机上应弹出通知；批准后 IF 节点的 true 分支亮起。

接口的完整说明见 [hub.md](../hub.md) 的 webhook 一节。
