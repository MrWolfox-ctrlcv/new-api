# 用户 API 聚合（BYOK）

已于 **2026-11-11** 上线 [OUA Relay「我的 API」](https://oua-cdn.wolfoxlabs.xyz/my-apis)，生产版本为 `v1.0.0-rc.48-wolfox-byok-99171a1f`。

本分支在 New API 开发版上增加用户自己的上游接入。控制台入口：**我的 API**（`/my-apis`）。

| 账户等级 | 上游接入数 | 每日 tokens |
| --- | ---: | ---: |
| 免费（非 vip 账户） | 1 | 10,000,000 |
| vip 账户分组 | 5 | 100,000,000 |

一组 Base URL 和 API 密钥算一个上游，其中可以配置多个模型。停用仍占名额，删除释放名额。VIP 身份由服务端账户分组决定，不能通过 API 密钥的计价分组获得；暂未增加订阅商品或支付流程。降级时保留配置，按创建时间仅使用新接入上限内的前几个上游。

每日用量在账户内汇总，按北京时间 00:00 分日；输入与输出相加，缓存输入属于输入 token，不重复累加。API 托管额度和平台余额分别计量。

## 客户端接入

1. 登录后进入“我的 API”，填写上游名称、HTTPS Base URL、上游密钥及模型 ID。
2. 在“API 密钥”创建或选用一个平台 API 密钥。
3. 将客户端 Base URL 设为 `https://你的域名/v1/hosted`，填写平台 API 密钥，选择已配置模型。

支持 OpenAI 兼容的 `/chat/completions`（文本、工具调用及 SSE）和 `/models`。当前不支持图片、音频、视频、Responses 或 Anthropic 原生协议。单次输出上限 32,768 tokens；未指定时使用 4,096；`n` 只能为 1；单个请求体上限 1 MiB，每个账户最多同时处理 16 个私有请求。

```sh
curl https://你的域名/v1/hosted/chat/completions \
  -H 'Authorization: Bearer sk-你的平台密钥' \
  -H 'Content-Type: application/json' \
  -d '{"model":"你的模型ID","messages":[{"role":"user","content":"你好"}],"max_tokens":1024}'
```

托管请求检查平台密钥的启用状态、有效期、IP 白名单及模型限制。使用自己的上游无需平台余额；平台余额及平台密钥的金额限制仅在平台备用路径中检查。

## 平台付费备用

**默认关闭，须用户确认并完成安全验证后主动开启。** 私有上游未成功或每日额度不足时，才可进入原有 TokenAuth → 分组选路 → Relay → 预扣及结算流程，按平台密钥所在分组的原有价格扣费。不会提高账户权限、绕过平台额度检查或修改管理员定价。响应已开始、usage 缺失、流中断、内部数据库失败或并发限流时，不自动转到付费备用。

响应头 `X-Hosted-Route: private` / `platform` 区分所走路径；平台鉴权或余额检查失败时仍会拒绝请求。原有 `/v1/chat/completions` 路径维持原行为。

## 用量与密钥

- 请求发送前按保守上限占用每日额度，成功后根据上游有效 `usage` 结算一次。明确的非成功 HTTP 响应退回预留量。
- 上游超时、网络状态不明、usage 缺失或不合法、客户端中断、进程退出时保留预留量，避免绕过限额。可用额度接近上限时，即使实际响应较短，也可能因无法预留而拒绝。
- `CRYPTO_SECRET`（或作为后备的 `SESSION_SECRET`）必须配置为至少 32 字符的稳定随机秘密；未配置时拒绝保存上游密钥。须备份并跨重启保持一致，不能随意更换。数据库只保存 AES-GCM 密文及末四位提示，不提供密钥明文读取接口。
- 添加、编辑、删除上游及切换备用均复用现有密码／MFA／Passkey／OAuth 安全验证；一次性证明绑定动作、上游 ID 和请求体摘要。
- 私有上游不写入公共渠道。服务器按登录身份限定查询和修改；出站只允许公网 HTTPS 的 443 端口，拒绝内网、元数据地址、代理环境变量和重定向，并在实际连接时检查 DNS 结果。

## 验证

开发环境：Go 1.26.10、Bun 1.4.4。数据库矩阵使用真实 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15，覆盖全新表、rc48 用户与令牌数据升级、重复迁移、并发上限、每日额度、重复结算和原有余额保护。

```sh
# DSN 必须指向本机一次性测试库；测试仅操作自动生成前缀的表。
BYOK_TEST_MYSQL_DSN='...' BYOK_TEST_POSTGRES_DSN='...' \
  go test ./model ./service ./controller ./middleware -run '^TestHosted' -count=1
cd web
bun check -b
bun node_modules/vitest/vitest.mjs run src/features/hosted-api/__tests__ src/features/auth/secure-verification/__tests__
bun run build
```

安全设计参考 [OWASP ASVS 5.0.0](https://asvs.owasp.org/)、[Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)、[SSRF Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Server_Side_Request_Forgery_Prevention_Cheat_Sheet.html) 和 [IDOR Prevention](https://cheatsheetseries.owasp.org/cheatsheets/Insecure_Direct_Object_Reference_Prevention_Cheat_Sheet.html)，验证对应的凭据隔离、过期、重放、越权及出站地址控制；不代表对整个上游项目的完整合规认证。

本次上线已备份原版本与数据库，使用最终 Linux 发布包在无外网隔离环境完成 16 项原有选路回归、聚合 HTTP 检查及 VIP 五个接入成功／第六个拒绝的检查。生产数据库副本连续启动两次，确认升级和重复迁移保留原有账户、余额、密钥、渠道及价格；公网登录和手机页面也已验证。

生产临时验证账户及密钥已清理，验证未调用真实推理上游或消耗平台计费额度。平台付费备用仍默认关闭，未自动升级任何账户为 VIP。后续发布仍须备份数据库和稳定的加密秘密；回退应用代码时保留新增托管表，不用历史数据库覆盖上线后的余额和请求记录。
