# Smart Router Web 控制台

苹果风格的智能路由网关管理界面（Vue 3 + Vite 组件化，明暗双主题）。

> 旧版单文件前端备份在 `web-legacy/`（如需回退，恢复 `web-legacy/index.html` 即可）。

## 快速开始

### 方式一：Gateway 同端口托管（生产/日常使用，推荐）

```bash
# 1. 构建前端
cd web && npm install && npm run build && cd ..

# 2. 启动 Gateway（自动托管 web/dist，无跨域问题）
./bin/gateway -config configs/config.local.yaml -web-dir web

# 3. 浏览器访问
open http://localhost:8080/
```

默认管理员 Key：`test-admin-key`（api_keys 表为空时首次启动自动创建）。

### 方式二：开发模式（Vite 热更新）

```bash
# 终端 1：启动后端
./bin/gateway -config configs/config.local.yaml

# 终端 2：启动 Vite 开发服务器（API 自动代理到 :8080）
cd web && npm install && npm run dev

# 浏览器访问
open http://localhost:5173/
```

## 页面结构

| 路由 | 页面 | 说明 |
|---|---|---|
| `#/` | 总览 | 24h 请求/成功率/延迟（同比）、趋势图、模型分布、告警、最近决策、**分组切换器** |
| `#/channels` | 站点 | 站点列表（**分组筛选 chips**、**余额徽章**）、详情（信息/健康/统计/**余额**）、新增/编辑（**多选分组**）、**分组管理**（增删改+策略/熔断/检测参数）、获取上游模型 |
| `#/playground` | 测试台 | 真实流式请求、**分组选择**（限定路由范围）、路由决策信息 |
| `#/decisions` | 决策 | 决策日志表格（含分组列）、**分组筛选**、详情抽屉 |
| `#/circuit` | 熔断 | 四态熔断器、**分组切换器**、重置 |
| `#/settings` | 设置 | 连接配置、API Keys（**分组绑定**）、运行配置、**默认测试模型**（从上游模型列表中选择）、**低余额告警阈值** |

## 项目结构

```
web/
├── index.html               # Vite 入口
├── vite.config.js           # 构建配置 + 开发代理
├── package.json
└── src/
    ├── main.js              # 应用入口 + 路由
    ├── App.vue              # 外壳（侧边栏 + 离线横幅）
    ├── api.js               # API 客户端（认证、错误处理、流式）
    ├── store.js             # 全局状态 + 主题 + Toast
    ├── utils.js             # 格式化工具
    ├── styles/
    │   ├── tokens.css       # 设计令牌（明暗双主题）
    │   └── base.css         # 基础样式与通用类
    ├── components/
    │   ├── AppSidebar.vue   # 毛玻璃侧边栏
    │   ├── BaseModal.vue    # 弹窗
    │   ├── BaseChart.vue    # ECharts 封装（主题自适应）
    │   ├── StatCard.vue     # 统计卡
    │   ├── EmptyState.vue   # 空状态
    │   ├── ToastHost.vue    # Toast 通知
    │   └── Icon.vue         # SF Symbols 风格图标集
    └── views/
        ├── DashboardView.vue
        ├── ChannelsView.vue
        ├── PlaygroundView.vue
        ├── DecisionsView.vue
        ├── CircuitView.vue
        └── SettingsView.vue
```

## 设计要点

- **苹果风设计语言**：`-apple-system` 字体栈、通透灰背景（#f5f5f7）、毛玻璃卡片（backdrop-filter）、大圆角、柔和阴影、蓝色主色（#0a84ff）
- **明暗双主题**：跟随系统（`prefers-color-scheme`），侧边栏可循环切换「自动 → 浅色 → 深色」，选择持久化到 localStorage
- **真数据无演示**：全部页面接真实后端接口（统计聚合、决策富化、熔断、API Keys CRUD）
- **真流式**：测试台使用 fetch + ReadableStream 解析 SSE，逐字渲染

## 与后端的接口对照

| 功能 | 接口 |
|---|---|
| 站点 CRUD | `GET/POST/PATCH/DELETE /admin/channels[/:id]` |
| 站点健康 | `GET /admin/health/:id` |
| 上游模型列表 | `GET /admin/channels/:id/models` · `POST /admin/upstream/models` |
| **分组 CRUD** | `GET/POST /admin/groups` · `PATCH/DELETE /admin/groups/:id` |
| 统计聚合 | `GET /admin/stats[?group_id=]` |
| 决策日志 | `GET /admin/decisions?limit=[&group_id=]` |
| 熔断 | `GET /admin/circuit[?group_id=]` · `POST /admin/circuit/:id/reset` |
| API Keys | `GET/POST /admin/keys` · `PATCH/DELETE /admin/keys/:id`（支持 group_ids 绑定） |
| 运行配置 | `GET /admin/config` |
| 测试请求 | `POST /v1/chat/completions`（body `group` 字段或 `X-Group` 头） |

## 中转站分组

- **扁平多对多分组**：一个站点可属于多个分组；请求可指定分组（body `group` 字段或 `X-Group` 头），网关只在组内站点中路由
- **分组级配置**：默认路由策略（策略查找链：Token×模型 → Token → 分组默认 → 系统默认）、熔断参数覆盖、健康检测间隔（存活/价格/探针/**余额**）与探针预算覆盖（0 = 跟随全局）
- **API Key 分组绑定**：caller Key 可绑定分组；请求未指定分组时自动限定在绑定组并集内，显式指定未绑定分组返回 403；admin 不受限
- **默认分组**：迁移自动创建「默认分组」并将存量站点归入；新建站点未选分组时自动归入
- **全链路记录**：决策日志与请求历史记录 group_id，统计/决策/熔断接口均支持按组筛选

## 余额自动检测

- **多协议探测**：优先站点自定义接口（`balance_api_url`，完整 URL 或路径）→ one-api/new-api（`/api/user/self`，Access Token）→ OpenAI 官方（`/v1/dashboard/billing/credit_grants`，API Key）；均不支持时标记"不可用"而非误报
- **自定义余额接口**：部分中转站的管理 API 不在标准路径或独立域名——在网页控制台 F12 → Network 找到余额请求地址，填入站点编辑表单的「余额接口地址」即可（响应格式自动识别：one-api `data.quota` / OpenAI `total_available` / 字符串数字）
- **自动调度**：checker 每 10 分钟检测一次（分组可覆盖 `balance_interval_seconds`）；新站点加入后立即检测
- **展示**：站点卡片余额徽章（≤$1 红色 / >$1 绿色）、站点详情「余额」页签（当前余额 + 历史折线 + 明细）
- **低余额告警**：余额 ≤ 阈值（设置页可配置，默认 $1）进入总览告警与侧边栏红点
- 相关接口：`GET /admin/channels/:id/balance` · `GET/PATCH /admin/settings`

## 常见问题

### 页面顶部出现红色离线横幅
- 确认 Gateway 已启动：`curl http://localhost:8080/health`
- 在「设置 → 连接」中修改 Gateway 地址或 API Key 后保存并重试

### 开发模式端口冲突
- 修改 `vite.config.js` 中的 `server.port` 和 `proxy` 目标地址

### 构建产物太大
- ECharts 单独分包（`echarts` chunk），刷新页面后浏览器会缓存；如需进一步减小可改为按需引入 ECharts 模块
