# Smart Router Web 控制台

苹果风格的智能路由网关管理界面（Vue 3 + Vite 组件化，明暗双主题）。

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
| `#/` | 总览 | 24h 请求/成功率/延迟（同比）、趋势图、模型分布、告警、**决策雷达**（最近一次决策六维对比 + 近 10 次最优站点，可点击切换）、**分组切换器**、站点综合信息抽屉 |
| `#/channels` | 站点 | 站点列表（**分组筛选**、**余额徽章**、OpenAI/Anthropic 协议徽章、中转站类型徽章、角色中文徽章）、详情（信息/健康/统计/余额/**倍率**）、新增/编辑（**接口协议**、**中转站类型**、**默认测试模型**、**余额自动登录邮箱密码**、多选分组、乐观锁、**测试连接**）、**分组管理**、**倍率检测分组**、获取上游模型 |
| `#/playground` | 测试台 | 真实流式请求、**站点选择自动预填该站点默认测试模型**、**分组选择**（限定路由范围且站点列表联动过滤）、路由决策信息、**请求中等待动画**（进行中状态条 + 三点弹跳 + 实时计时） |
| `#/decisions` | 决策 | 决策日志表格（含分组列）、**分组筛选**、详情抽屉（候选**六维评分雷达图** + 真实指标/人话排名/排除原因/故障切换明细）、**编辑模式多选/全选删除与导出** |
| `#/strategy` | 策略中心 | 路由策略按「系统默认」与**每个分组**分别配置；5 种策略可视化卡片（因素权重条）、「加权均衡」四维权重滑块、分组一键恢复跟随系统默认 |
| `#/circuit` | 熔断 | 四态熔断器（分组隔离）、**分组切换器**、重置 |
| `#/alerts` | 告警 | **全部活跃告警统一视图**（低余额/倍率超标/熔断开闸降级/站点禁用/价格同步失败），摘要卡 + 按严重度排序列表、**分组切换器**、30s 自动刷新、按类型跳转处理页 |
| `#/settings` | 设置 | 连接配置、API Keys（**分组绑定**）、**每站点默认测试模型**（表格逐站点设置）、**官方模型价格库**、**低余额告警阈值** |

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
    │   ├── AppSidebar.vue    # 毛玻璃侧边栏（含告警徽标）
    │   ├── BaseModal.vue     # 弹窗（ESC 关闭 / 焦点陷阱 / aria 属性 / 滚动锁定）
    │   ├── ConfirmDialog.vue # 确认对话框（替代原生 confirm，支持 danger 态）
    │   ├── BaseChart.vue     # ECharts 封装（主题自适应 / tooltip 挂 body 逃逸裁剪 / 渲染去重）
    │   ├── RadarChart.vue    # 手绘 SVG 六维雷达图（决策候选评分/总览决策雷达）
    │   ├── StatCard.vue      # 统计卡
    │   ├── GroupSwitcher.vue # 分组切换器（胶囊模式 / compact 下拉模式）
    │   ├── SelectBox.vue     # 通用下拉选择器（键盘导航/自适应弹层/选中对勾）
    │   ├── AlertPopupHost.vue# 右下角预警弹窗（仅 critical 级，弹跳动效 + 倒计时 + 跳转告警页）
    │   ├── EmptyState.vue    # 空状态
    │   ├── ToastHost.vue     # Toast 通知
    │   └── Icon.vue          # SF Symbols 风格图标集
    └── views/
        ├── DashboardView.vue
        ├── ChannelsView.vue
        ├── PlaygroundView.vue
        ├── DecisionsView.vue
        ├── StrategyView.vue
        ├── CircuitView.vue
        ├── AlertsView.vue
        └── SettingsView.vue
```

## 设计要点

- **苹果风设计语言**：`-apple-system` 字体栈、通透灰背景（#f5f5f7）、毛玻璃卡片（backdrop-filter）、大圆角、柔和阴影、蓝色主色（#0a84ff）
- **明暗双主题**：跟随系统（`prefers-color-scheme`），侧边栏可循环切换「自动 → 浅色 → 深色」，选择持久化到 localStorage
- **真数据无演示**：全部页面接真实后端接口（统计聚合、决策富化、熔断、API Keys CRUD）
- **真流式**：测试台使用 fetch + ReadableStream 解析 SSE，逐字渲染
- **统一下拉体验**：全站下拉选择器由 `SelectBox.vue` 统一提供（弹层式选项、键盘导航、自适应方向、选中对勾）
- **告警弹窗**：全局轮询系统告警，**仅 critical 级**新告警/严重度升级时从右下角弹出预警卡片（弹跳动效 + 倒计时进度条），可一键跳转告警页；全部告警在「告警」页统一查看
- **图表 tooltip**：BaseChart 统一将 tooltip 挂到 body 逃逸 overflow 裁剪（迷你图/滚动容器内不再被切），滚动祖先容器时自动收起，渲染去重避免刷新闪掉悬停
- **可访问性**：全局 `:focus-visible` 焦点环（`--focus-ring` 令牌）保证键盘导航可见；弹窗支持 ESC 关闭与焦点陷阱；三级文字色满足 WCAG AA 4.5:1 对比度；破坏性操作统一走 `ConfirmDialog` 而非原生 `confirm()`
- **生产安全**：生产构建不预填开发 Key（凭据存 sessionStorage）；中文响应头由网关 URI 编码、前端解码还原

## 与后端的接口对照

| 功能 | 接口 |
|---|---|
| 站点 CRUD | `GET/POST/PATCH/DELETE /admin/channels[/:id]`（含 protocol / relay_type / test_model 等字段） |
| 站点健康 | `GET /admin/health/:id` |
| 上游模型列表 | `GET /admin/channels/:id/models` · `POST /admin/upstream/models`（按协议发送认证头） |
| **实时倍率** | `GET /admin/channels/:id/ratio`（声明/实测/历史/**分组**） · `POST /admin/channels/:id/probe-ratio`（按需实测） · `POST/PATCH/DELETE /admin/channels/:id/ratio-groups[/:gid]` · `POST .../ratio-groups/:gid/probe` |
| **官方价格库** | `GET/POST /admin/model-prices` · `DELETE /admin/model-prices/:model` |
| **分组 CRUD** | `GET/POST /admin/groups` · `PATCH/DELETE /admin/groups/:id` |
| **策略中心** | `GET/PUT /admin/policies`（系统默认策略） · `GET/PUT /admin/groups/:id/strategy`（每分组策略与权重） |
| 统计聚合 | `GET /admin/stats[?group_id=]` · `GET /admin/channel-metrics` |
| 告警列表 | `GET /admin/alerts[?group_id=]`（全部活跃告警） |
| 决策日志 | `GET /admin/decisions?limit=[&group_id=]` · `DELETE /admin/decisions`（批量删除） |
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

## 接口协议与中转站类型

- **接口协议**（站点级）：`openai`（OpenAI 兼容，默认）/ `anthropic`（Claude 原生）。网关对外保持 OpenAI 接口；anthropic 站点由后端 `internal/protocol` 完成请求/响应/SSE 流式双向转换与 `x-api-key` 认证，前端聊天端点/测试连接/模型列表均按协议自动适配
- **中转站类型**（站点级）：`newapi`（new-api/one-api 系）/ `sub2api`（Sub2API）/ `custom`。表单中选择类型后自动填入默认余额接口地址（new-api → `/api/user/self`，sub2api → `/api/v1/auth/me`），可手动覆盖
- **默认测试模型**（站点级）：设置页「请求测试台设置」表格逐站点配置；测试台选择站点后自动预填该模型，模型下拉只提示该站点的已映射模型

## 余额自动检测

- **多协议探测**：优先站点自定义接口（`balance_api_url`，完整 URL 或路径）→ 中转站类型默认接口 → one-api/new-api（`/api/user/self`，Access Token）→ OpenAI 官方（`/v1/dashboard/billing/credit_grants`，API Key）；均不支持时标记"不可用"而非误报
- **Sub2API 余额自动登录**：sub2api 站点可配置余额登录邮箱/密码，checker 自动登录换取会话 JWT（Redis 缓存，401 自动重登），免手动抓包令牌
- **响应格式自动识别**：one-api `data.quota` / new-api 会话嵌套 `data.user.quota`（quota 单位自动换算美元，1 USD = 500,000 quota）→ `data.balance`（美元）→ OpenAI `total_available`；仅支持 POST 的接口 GET 失败自动回退 POST
- **自定义余额接口**：部分中转站的管理 API 不在标准路径或独立域名——在网页控制台 F12 → Network 找到余额请求地址，填入站点编辑表单的「余额接口地址」即可；401/403 时界面提示令牌可能已过期
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
