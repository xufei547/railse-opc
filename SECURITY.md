# 安全说明

[English](#security)

## 报告漏洞

发邮件到 xufei547@gmail.com，不要开公开 issue。写上版本（`railse version` 的输出）、复现步骤和能造成什么后果。一周内回复。修好之前请不要公开细节。

## 支持的版本

只修最新的发布版，修复随新版本发出。

## 设计内的取舍

下面这些是有意这样做的，细节见 [docs/hub.md](docs/hub.md) 的「安全要点」。如果实际比这里写的更糟（比如回调结束了令牌还在库里），那是漏洞，请报告。

- **hub 里的凭据大多只存哈希**：机器密钥、登录会话、webhook 来源令牌、接入码只存 SHA-256，网页密码用 argon2id。
- **有几样明文存**：都要用原值，没法只存哈希。备份和拷贝时当私密数据保管。
  - hub 数据目录的 `hub.db` 里有 VAPID 私钥和推送订阅的密钥，发推送要用。
  - webhook 的出站回调令牌（`callback_token`）回调时要原样带上，存到回调结束（成功、3 次都失败或 hub 关停中止）就清空。
  - 用 `--acme-domain` 时，ACME 账户私钥和证书私钥明文存在数据目录的 `acme/`。
  - 工作机的 `~/.railse/machine.json`（0600）里是机器密钥，连 hub 要用。
- **脱敏只认得出规则里有的**：命令和文本离开本机前，按 `internal/redact` 的规则把令牌、密码换成 `<redacted>`，hub 收到后再抹一次。
  - 规则没认出的密钥会原样进 hub 和手机。请求的 `has_redacted`（网页上「含被隐藏的部分」的提示）只表示认出了一些，不表示抹干净了。
  - 抹掉的部分可能比密钥长，连带盖住紧跟着的一截命令。只要有抹掉的部分，网页就醒目提示，hub 也不让「记住」。
  - 本机审计 `~/.railse/audit.jsonl` 存的是脱敏后的载荷与消息，不存原文。
- **hub 不能让机器执行命令**：hub 发给守护进程的只有对请求的放行或拒绝、规则和暂停开关，没有下发命令的通道。但登进 hub 网页的人能放行送上来的任何请求，也能导入规则，导入的规则可以放行 `git push`、递归删除这类「记住」记不成放行的操作。所以 hub 的密码和数据目录要保管好。

# Security

## Reporting a vulnerability

Email xufei547@gmail.com. Don't open a public issue. Include the version (output of `railse version`), steps to reproduce, and what an attacker could do with it. You'll get a reply within a week. Please don't publish details until a fix is out.

## Supported versions

Only the latest release gets fixes. Fixes ship in a new release.

## Trade-offs by design

These are deliberate. The details are in the "安全要点" (security) section of [docs/hub.md](docs/hub.md), which is in Chinese. If the real behavior is worse than described here (say, a callback token is still in the database after the callback finished), that's a vulnerability. Please report it.

- **Most credentials on the hub are stored as hashes.** Machine secrets, login sessions, webhook source tokens and enrollment codes are stored as SHA-256 hashes. The web login password is hashed with argon2id.
- **A few things are stored in plaintext.** They're needed as-is, so a hash won't do. Treat them as private when you back up or copy them.
  - `hub.db` in the hub's data directory holds the VAPID private key and the push subscription keys; sending a push needs them.
  - The outgoing webhook callback token (`callback_token`) has to be sent back as-is. It's kept until the callback ends (it succeeds, all 3 attempts fail, or the hub shuts down mid-callback) and then cleared.
  - With `--acme-domain`, the ACME account key and the certificate's private key are stored in plaintext in `acme/` in the data directory.
  - On work machines, `~/.railse/machine.json` (mode 0600) holds the machine secret the daemon uses to connect to the hub.
- **Redaction only catches what its rules know.** Before commands and text leave the machine, the rules in `internal/redact` replace tokens and passwords with `<redacted>`, and the hub redacts again when they arrive.
  - A secret the rules don't recognize reaches the hub and the phone unchanged. A request's `has_redacted` (the "含被隐藏的部分" warning in the web app) only means something was caught, not that everything was.
  - The hidden part can be longer than the secret and cover part of the command right after it. Whenever anything was redacted, the web app shows a prominent warning and the hub won't let you remember the request.
  - The local audit log, `~/.railse/audit.jsonl`, stores the redacted versions of payloads and messages, not the originals.
- **The hub can't make a machine run commands.** All it sends to a daemon is allow or deny for a request, rules, and the pause switch. There's no channel for commands. But anyone who can log in to the hub's web app can approve any request that reaches it, and import rules, including ones that allow `git push` or recursive deletes. Remembering a decision never creates rules like that. So protect the hub's password and data directory.
